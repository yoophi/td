package issuestore

import (
	"context"
	"errors"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/reviewpolicy"
	"testing"
	"time"
)

type overviewFixture struct {
	records []ghstore.Record
	facts   map[string]*ghstore.MonitorReviewFacts
	failure error
}

func (f overviewFixture) List(ctx context.Context, all bool) ([]ghstore.Record, error) {
	if !all {
		panic("overview must include closed issues")
	}
	return f.records, f.failure
}
func (f overviewFixture) ObserveMonitorReview(ctx context.Context, r *ghstore.Record, actor string) (*ghstore.MonitorReviewFacts, error) {
	return f.facts[r.ID], f.failure
}

func TestGitHubOverviewCountsAndReviewPolicy(t *testing.T) {
	now := time.Now()
	f := overviewFixture{facts: map[string]*ghstore.MonitorReviewFacts{}}
	for n, status := range []models.Status{models.StatusOpen, models.StatusClosed, models.StatusInProgress, models.StatusInReview, models.StatusInReview, models.StatusInReview} {
		id := string(rune('a' + n))
		f.records = append(f.records, ghstore.Record{Issue: models.Issue{ID: id, Status: status, Type: models.TypeTask, Priority: models.PriorityP1}})
	}
	f.records[3].ImplementerSession = "actor"
	f.facts["d"] = &ghstore.MonitorReviewFacts{Fresh: true, ImplementationInvolved: true, AnyInvolved: true}
	f.facts["e"] = &ghstore.MonitorReviewFacts{Fresh: true, ActiveApproval: true}
	f.facts["f"] = &ghstore.MonitorReviewFacts{Fresh: false}
	f.records = append(f.records, ghstore.Record{Issue: models.Issue{ID: "deleted", Status: models.StatusOpen, DeletedAt: &now}})
	for _, tc := range []struct {
		mode          reviewpolicy.Mode
		review, close int
	}{{reviewpolicy.ModeTrusted, 1, 1}, {reviewpolicy.ModeDelegated, 0, 1}, {reviewpolicy.ModeStrict, 1, 0}, {reviewpolicy.ModeBalanced, 1, 0}} {
		r, err := ReadGitHubOverview(context.Background(), f, "actor", tc.mode)
		if err != nil || r.Issues["total"] != 6 || r.ByPriority["P1"] != 6 || r.ByType["task"] != 6 || r.ReviewQueue["awaiting_review"] != 3 || r.ReviewQueue["stale_review"] != 1 || r.ReviewQueue["you_can_review"] != tc.review || r.ReviewQueue["you_can_close_after"] != tc.close {
			t.Fatalf("%s %+v %v", tc.mode, r, err)
		}
	}
}

func TestGitHubOverviewEmptyAndFailureNeverReturnsPartialCounts(t *testing.T) {
	r, err := ReadGitHubOverview(context.Background(), overviewFixture{}, "actor", reviewpolicy.ModeTrusted)
	if err != nil || r.Issues["total"] != 0 || len(r.ByPriority) != 5 {
		t.Fatalf("%+v %v", r, err)
	}
	for _, f := range []overviewFixture{{failure: errors.New("rate limit")}, {records: []ghstore.Record{{Issue: models.Issue{ID: "same"}}, {Issue: models.Issue{ID: "same"}}}}, {records: []ghstore.Record{{Issue: models.Issue{ID: "review", Status: models.StatusInReview}}}}} {
		r, err := ReadGitHubOverview(context.Background(), f, "actor", reviewpolicy.ModeTrusted)
		if err == nil || r != nil {
			t.Fatalf("partial result %+v %v", r, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if r, err := ReadGitHubOverview(ctx, overviewFixture{}, "actor", reviewpolicy.ModeTrusted); !errors.Is(err, context.Canceled) || r != nil {
		t.Fatalf("%+v %v", r, err)
	}
}

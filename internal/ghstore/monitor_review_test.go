package ghstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/reviewpolicy"
	"strings"
	"testing"
)

func TestUnverifiedReviewQueuesNeverGrantRecordedApproval(t *testing.T) {
	f := newReviewFixture(t)
	f.transition(t, "review", TransitionOptions{SessionID: "worker", Mode: reviewpolicy.ModeTrusted})
	f.transition(t, "approve", TransitionOptions{SessionID: "worker", Mode: reviewpolicy.ModeTrusted, RecordOnly: true, SelfReview: true, Reason: "Fixture self-review"})
	record, err := f.client.Get(context.Background(), "1")
	if err != nil {
		t.Fatal(err)
	}
	f.client.run = func(context.Context, string, []byte, ...string) ([]byte, error) {
		panic("queue display made a remote call")
	}
	for _, actor := range []string{"worker", "independent"} {
		facts, err := UnverifiedMonitorReview(record, actor)
		if err != nil || facts.Fresh || facts.ActiveApproval || facts.AnyInvolved != (actor == "worker") {
			t.Fatalf("%+v %v", facts, err)
		}
	}
	for _, r := range []*Record{nil, {Issue: models.Issue{ID: "bad", Status: models.StatusInReview}}, {Issue: models.Issue{ID: "gh-1", Status: models.StatusClosed}}} {
		if _, err := UnverifiedMonitorReview(r, "worker"); err == nil {
			t.Fatal("invalid queue observation accepted")
		}
	}
}

func TestBulkReviewQueueCostDoesNotGrowWithReviewCount(t *testing.T) {
	f := newReviewFixture(t)
	f.transition(t, "review", TransitionOptions{SessionID: "worker", Mode: reviewpolicy.ModeTrusted})
	raw, err := fixtureIssueJSON(f.issue)
	if err != nil {
		t.Fatal(err)
	}
	var template apiIssue
	if err := json.Unmarshal(raw, &template); err != nil {
		t.Fatal(err)
	}
	for _, count := range []int{1, 60} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			issues := [][]apiIssue{{}}
			for n := 1; n <= count; n++ {
				next := template
				next.Number = n
				issues[0] = append(issues[0], next)
			}
			client, calls := snapshotFixture(issues, [][]apiComment{{}}, nil)
			snapshot, err := client.ReadSnapshot(context.Background(), true)
			if err != nil {
				t.Fatal(err)
			}
			for _, record := range snapshot.Issues(false) {
				facts, err := UnverifiedMonitorReview(&record, "independent")
				if err != nil || facts.Fresh || facts.ActiveApproval {
					t.Fatalf("%+v %v", facts, err)
				}
			}
			if len(*calls) != 2 {
				t.Fatalf("queue cost grew: %v", *calls)
			}
		})
	}
}

func TestMonitorReviewFactsApprovalStalenessAndRetainedParticipation(t *testing.T) {
	f := newReviewFixture(t)
	ctx := context.Background()
	f.transition(t, "start", TransitionOptions{SessionID: "fixture-worker", Mode: reviewpolicy.ModeTrusted})
	f.transition(t, "review", TransitionOptions{SessionID: "fixture-worker", Mode: reviewpolicy.ModeTrusted})
	f.transition(t, "approve", TransitionOptions{SessionID: "fixture-worker", Mode: reviewpolicy.ModeTrusted, RecordOnly: true, SelfReview: true, Reason: "Fixture self-review"})
	observed, err := f.client.Get(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	before := f.writes
	facts, err := f.client.ObserveMonitorReview(ctx, observed, "fixture-worker")
	if err != nil || !facts.Fresh || !facts.ActiveApproval || !facts.ImplementationInvolved || !facts.AnyInvolved || f.writes != before {
		t.Fatalf("%+v %v writes=%d", facts, err, f.writes-before)
	}
	other, err := f.client.ObserveMonitorReview(ctx, observed, "fixture-independent")
	if err != nil || !other.Fresh || !other.ActiveApproval || other.ImplementationInvolved || other.AnyInvolved {
		t.Fatalf("independent: %+v %v", other, err)
	}
	f.issue["title"] = "Edited since approval"
	observed, err = f.client.Get(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	facts, err = f.client.ObserveMonitorReview(ctx, observed, "fixture-worker")
	if err != nil || facts.Fresh || facts.ActiveApproval || !facts.ImplementationInvolved {
		t.Fatalf("stale: %+v %v", facts, err)
	}
	f.issue["title"] = "Fixture"
	f.nativeState("closed")
	f.nativeState("open")
	observed, err = f.client.Get(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	facts, err = f.client.ObserveMonitorReview(ctx, observed, "fixture-worker")
	if err != nil || facts.Fresh || facts.ActiveApproval {
		t.Fatalf("native state history revived approval: %+v %v", facts, err)
	}
	if f.writes != before {
		t.Fatal("read changed approval")
	}
}
func TestMonitorReviewFactsPropagatesTransportConflictAndInvalidObservation(t *testing.T) {
	f := newReviewFixture(t)
	ctx := context.Background()
	f.transition(t, "review", TransitionOptions{SessionID: "fixture-worker", Mode: reviewpolicy.ModeTrusted})
	observed, err := f.client.Get(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	base := f.client.run
	f.client.run = func(ctx context.Context, dir string, input []byte, args ...string) ([]byte, error) {
		if strings.Contains(args[5], "/events?") {
			return nil, errors.New("events permission denied")
		}
		return base(ctx, dir, input, args...)
	}
	if facts, err := f.client.ObserveMonitorReview(ctx, observed, "fixture-independent"); err == nil || facts != nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("hidden read failure: %+v %v", facts, err)
	}
	f.client.run = base
	f.issue["title"] = "Concurrent change"
	if facts, err := f.client.ObserveMonitorReview(ctx, observed, "fixture-independent"); err == nil || facts != nil {
		t.Fatal("stale observation returned actionable facts")
	}
	copy := *observed
	copy.repository = "wrong/repo"
	for _, record := range []*Record{nil, &copy} {
		if _, err := f.client.ObserveMonitorReview(ctx, record, "fixture-worker"); err == nil {
			t.Fatal("untrusted observation accepted")
		}
	}
	if _, err := f.client.ObserveMonitorReview(ctx, observed, " "); err == nil {
		t.Fatal("blank actor accepted")
	}
}

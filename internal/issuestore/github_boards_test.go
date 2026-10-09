package issuestore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
)

type boardReaderFixture struct {
	records    []ghstore.Record
	expression string
	err        error
}

func (f *boardReaderFixture) List(context.Context, bool) ([]ghstore.Record, error) {
	return f.records, f.err
}
func (f *boardReaderFixture) BoardQueryObserved(*ghstore.BoardRecord) (string, error) {
	return f.expression, nil
}
func (f *boardReaderFixture) ListActivity(context.Context, string) ([]models.Activity, error) {
	return nil, errors.New("activity unavailable")
}
func (f *boardReaderFixture) AppendActivity(context.Context, string, models.Activity) (*models.Activity, error) {
	panic("read wrote activity")
}

func TestGitHubBoardScheduleQueryMatchesSnapshot(t *testing.T) {
	today := time.Now().Format("2006-01-02")
	future := time.Now().AddDate(0, 0, 1).Format("2006-01-02")
	f := &boardReaderFixture{expression: "due <= today AND (defer = NULL OR defer <= today)", records: []ghstore.Record{{Issue: models.Issue{ID: "gh-1", Status: models.StatusOpen, DueDate: &today}}, {Issue: models.Issue{ID: "gh-2", Status: models.StatusOpen, DueDate: &today, DeferUntil: &future}}, {Issue: models.Issue{ID: "gh-3", Status: models.StatusClosed, DueDate: &today}}}}
	got, err := GitHubBoardCandidates(context.Background(), f, &ghstore.BoardRecord{}, false)
	if err != nil || len(got) != 1 || got[0].ID != "gh-1" {
		t.Fatal(got, err)
	}
}

func TestGitHubBoardCandidatesQueryAndStatusScope(t *testing.T) {
	now := time.Now().UTC()
	f := &boardReaderFixture{records: []ghstore.Record{
		{Issue: models.Issue{ID: "gh-1", Priority: models.PriorityP1, Status: models.StatusOpen, Points: 3}},
		{Issue: models.Issue{ID: "gh-2", Priority: models.PriorityP0, Status: models.StatusClosed, Points: 2}},
		{Issue: models.Issue{ID: "gh-3", Priority: models.PriorityP2, Status: models.StatusBlocked, Points: 1}},
		{Issue: models.Issue{ID: "gh-4", Priority: models.PriorityP0, Status: models.StatusOpen, DeletedAt: &now, Points: 9}},
	}}
	b := &ghstore.BoardRecord{}
	ctx := context.Background()
	got, err := GitHubBoardCandidates(ctx, f, b, false)
	if err != nil || len(got) != 2 || got[0].ID != "gh-1" || got[1].ID != "gh-3" {
		t.Fatalf("%+v %v", got, err)
	}
	f.expression = "points >= 2 sort:-points"
	got, err = GitHubBoardCandidates(ctx, f, b, true)
	if err != nil || len(got) != 2 || got[0].ID != "gh-1" || got[1].ID != "gh-2" {
		t.Fatalf("%+v %v", got, err)
	}
	got, err = GitHubBoardCandidates(ctx, f, b, false, "gh-2", "gh-3", "gh-4")
	if err != nil || len(got) != 2 {
		t.Fatalf("keep bypassed query/deletion: %+v %v", got, err)
	}
	f.err = errors.New("HTTP 403 permission denied")
	if _, err = GitHubBoardCandidates(ctx, f, b, true); err == nil {
		t.Fatal("permission failure hidden")
	}
	f.err = nil
	f.expression = "future_field = x"
	if _, err = GitHubBoardCandidates(ctx, f, b, true); err == nil {
		t.Fatal("invalid query hidden")
	}
	f.expression = "comment.text ~ failure"
	if _, err = GitHubBoardCandidates(ctx, f, b, true); err == nil {
		t.Fatal("cross-entity failure hidden")
	}
}

func TestGitHubBoardCandidatesCancellationAndDuplicateListing(t *testing.T) {
	f := &boardReaderFixture{records: []ghstore.Record{{Issue: models.Issue{ID: "gh-1", Priority: models.PriorityP2, Status: models.StatusOpen}}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := GitHubBoardCandidates(ctx, f, &ghstore.BoardRecord{}, true); err == nil {
		t.Fatal("cancelled read succeeded")
	}
	f.records = append(f.records, f.records[0])
	if _, err := GitHubBoardCandidates(context.Background(), f, &ghstore.BoardRecord{}, true); err == nil {
		t.Fatal("duplicate listing accepted")
	}
}

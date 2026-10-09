package monitor

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
)

type detailFixture struct {
	monitorDataFixture
	root            *ghstore.Record
	activityReads   int
	fresh, approval bool
	reviewErr       error
}

func (f *detailFixture) Get(context.Context, string) (*ghstore.Record, error) { return f.root, nil }
func (f *detailFixture) ListActivity(ctx context.Context, id string) ([]models.Activity, error) {
	f.activityReads++
	return f.ListActivityIncludingDeleted(ctx, id)
}
func (f *detailFixture) ObserveMonitorReview(_ context.Context, _ *ghstore.Record, actor string) (*ghstore.MonitorReviewFacts, error) {
	f.actor = actor
	return &ghstore.MonitorReviewFacts{Fresh: f.fresh, ActiveApproval: f.approval}, f.reviewErr
}
func detailSource(t *testing.T, f *detailFixture) *GitHubDataSource {
	t.Helper()
	return &GitHubDataSource{ctx: context.Background(), baseDir: t.TempDir(), actor: "actual-monitor", open: func(context.Context) (GitHubMonitorReader, error) { return f, nil }}
}
func TestGitHubMonitorDetailsRelationshipsActivitiesAndReviewFreshness(t *testing.T) {
	root := &ghstore.Record{Issue: models.Issue{ID: "gh-1", Title: "Direct root", Status: models.StatusInReview, ParentID: "gh-2"}, Details: &ghstore.IssueDetails{Dependencies: []string{"gh-3"}, Reviews: []models.IssueReview{{ID: "review-1", Decision: "approved"}}}}
	f := &detailFixture{root: root, fresh: true, approval: true}
	f.records = []ghstore.Record{{Issue: models.Issue{ID: "gh-1", Title: "Stale listing"}}, {Issue: models.Issue{ID: "gh-2", Type: models.TypeEpic}}, {Issue: models.Issue{ID: "gh-3", Status: models.StatusClosed}}, {Issue: models.Issue{ID: "gh-4"}, Details: &ghstore.IssueDetails{Dependencies: []string{"gh-1"}}}}
	f.activity = map[string][]models.Activity{"gh-1": {{ID: "comment-1", Kind: "comment", IssueID: "gh-1", Message: "note"}, {ID: "handoff-1", Kind: "handoff", IssueID: "gh-1", Done: []string{"ready"}, CreatedAt: time.Now()}}}
	for i := 0; i < 25; i++ {
		f.activity["gh-1"] = append(f.activity["gh-1"], models.Activity{ID: fmt.Sprintf("log-%d", i), Kind: "log", IssueID: "gh-1", CreatedAt: time.Now().Add(time.Duration(i) * time.Second)})
	}
	s := detailSource(t, f)
	msg := Model{DataSource: s}.fetchIssueDetails("gh-1")().(IssueDetailsMsg)
	if msg.Error != nil || msg.Issue.Title != "Direct root" || msg.ParentEpic.ID != "gh-2" || len(msg.BlockedBy) != 1 || msg.BlockedBy[0].ID != "gh-3" || len(msg.Blocks) != 1 || msg.Blocks[0].ID != "gh-4" || len(msg.Logs) != 20 || len(msg.Comments) != 1 || msg.Handoff == nil || !msg.HasActiveApproval || len(msg.Reviews) != 1 || f.actor != "actual-monitor" || f.activityReads != 1 {
		t.Fatalf("%+v", msg)
	}
	f.fresh = false
	if msg = s.Details("gh-1"); msg.Error != nil || msg.HasActiveApproval {
		t.Fatal("stale approval shown fresh")
	}
	f.reviewErr = &ghstore.ConflictError{ID: "gh-1"}
	if msg = s.Details("gh-1"); msg.Error == nil || msg.Issue != nil {
		t.Fatal("review conflict exposed partial details")
	}
}
func TestGitHubMonitorDetailsEpicLagDeletedAndBrokenReferences(t *testing.T) {
	now := time.Now()
	f := &detailFixture{root: &ghstore.Record{Issue: models.Issue{ID: "gh-1", Type: models.TypeEpic, Status: models.StatusOpen}}}
	f.records = []ghstore.Record{{Issue: models.Issue{ID: "gh-2", ParentID: "gh-1", Priority: models.PriorityP2}}, {Issue: models.Issue{ID: "gh-3", ParentID: "gh-1", Priority: models.PriorityP0}}, {Issue: models.Issue{ID: "gh-4", ParentID: "gh-1", DeletedAt: &now}}}
	s := detailSource(t, f)
	msg := s.Details("gh-1")
	if msg.Error != nil || len(msg.EpicTasks) != 2 || msg.EpicTasks[0].ID != "gh-3" {
		t.Fatalf("%+v", msg)
	}
	f.root.Details = &ghstore.IssueDetails{Dependencies: []string{"gh-4"}}
	if msg = s.Details("gh-1"); msg.Error == nil || msg.Issue != nil {
		t.Fatal("deleted dependency hidden")
	}
	f.root.Details = nil
	f.root.ParentID = "gh-99"
	if msg = s.Details("gh-1"); msg.Error == nil {
		t.Fatal("missing parent hidden")
	}
	f.root.ParentID = ""
	f.activityErr = errors.New("activity denied")
	if msg = s.Details("gh-1"); msg.Error == nil || msg.Issue != nil {
		t.Fatal("activity failure exposed partial result")
	}
	f.activityErr = nil
	f.root.DeletedAt = &now
	if msg = s.Details("gh-1"); msg.Error == nil {
		t.Fatal("deleted root accepted")
	}
}
func TestGitHubMonitorDetailsFailurePreservesModalAndValidatesBeforeOpening(t *testing.T) {
	issue := &models.Issue{ID: "gh-1", Title: "Retain"}
	m := Model{ModalStack: []ModalEntry{{IssueID: "gh-1", Issue: issue, Loading: true, Comments: []models.Comment{{Text: "retained"}}}}}
	result, cmd := m.Update(IssueDetailsMsg{IssueID: "gh-1", Error: errors.New("unavailable")})
	next := result.(Model)
	modal := next.CurrentModal()
	if cmd != nil || modal.Loading || modal.Error == nil || modal.Issue != issue || len(modal.Comments) != 1 {
		t.Fatal("detail refresh cleared prior modal")
	}
	s := &GitHubDataSource{ctx: context.Background(), actor: "actual", open: func(context.Context) (GitHubMonitorReader, error) {
		t.Fatal("invalid ID opened store")
		return nil, nil
	}}
	if msg := s.Details("1"); msg.Error == nil {
		t.Fatal("alias accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.ctx = ctx
	if msg := s.Details("gh-1"); !errors.Is(msg.Error, context.Canceled) {
		t.Fatalf("%+v", msg)
	}
	if msg := (Model{DataSource: dashboardOnlyFixture{}}).fetchIssueDetails("invalid")().(IssueDetailsMsg); msg.Error == nil {
		t.Fatal("missing detail adapter fell back to sqlite")
	}
}

type dashboardOnlyFixture struct{}

func (dashboardOnlyFixture) Fetch(string, bool, SortMode) RefreshDataMsg { return RefreshDataMsg{} }

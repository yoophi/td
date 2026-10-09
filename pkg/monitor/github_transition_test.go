package monitor

import (
	"context"
	"errors"
	"testing"

	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/reviewpolicy"
)

type transitionFixture struct {
	detailFixture
	writes           int
	original, action string
	options          ghstore.TransitionOptions
	failure          error
}

func (f *transitionFixture) TransitionObservedWithCascades(_ context.Context, r *ghstore.Record, action string, o ghstore.TransitionOptions) (*ghstore.Record, bool, error) {
	f.original = r.Title
	f.options = o
	f.action = action
	if r.Title != f.root.Title {
		return nil, false, &ghstore.ConflictError{ID: r.ID}
	}
	f.writes++
	return r, false, f.failure
}
func TestGitHubMonitorTransitionUsesOriginalDetailAndDashboardObservations(t *testing.T) {
	f := &transitionFixture{detailFixture: detailFixture{root: &ghstore.Record{Issue: models.Issue{ID: "gh-1", Title: "Original", Status: models.StatusInProgress}}}}
	f.records = []ghstore.Record{*f.root}
	s := &GitHubDataSource{ctx: context.Background(), baseDir: t.TempDir(), actor: "actual-monitor", open: func(context.Context) (GitHubMonitorReader, error) { return f, nil }, focus: func(context.Context) (*string, error) { return nil, nil }}
	for _, path := range []string{"details", "dashboard"} {
		f.root.Title = "Original"
		f.records = []ghstore.Record{*f.root}
		f.writes = 0
		var handles map[string]MonitorTransitionStore
		if path == "details" {
			msg := s.Details("gh-1")
			if msg.Error != nil {
				t.Fatal(msg.Error)
			}
			handles = msg.Transitions
		} else {
			msg := s.Fetch("", false, SortByPriority)
			if msg.Error != nil {
				t.Fatal(msg.Error)
			}
			handles = msg.Transitions
		}
		f.root.Title = "Peer"
		var conflict *ghstore.ConflictError
		if err := handles["gh-1"].Transition("review"); !errors.As(err, &conflict) || f.writes != 0 || f.original != "Original" {
			t.Fatalf("%s lost observation %v", path, err)
		}
	}
	msg := s.Details("gh-1")
	if err := msg.Transitions["gh-1"].Transition("reopen"); err != nil || f.writes != 1 || f.options.SessionID != "actual-monitor" || f.options.AgentType != "monitor" || f.options.Mode != reviewpolicy.ModeTrusted || f.action != "reopen" {
		t.Fatalf("transition %v %+v", err, f.options)
	}
	if err := msg.Transitions["gh-1"].Transition("approve"); err == nil || f.writes != 1 {
		t.Fatal("unconnected transition wrote")
	}
	f.failure = errors.New("partial cascade")
	if err := msg.Transitions["gh-1"].Transition("review"); !errors.Is(err, f.failure) || f.writes != 2 {
		t.Fatal("partial failure hidden or retried")
	}
}

type modelTransitionFixture struct {
	calls   int
	action  string
	failure error
}

func (f *modelTransitionFixture) Transition(action string) error {
	f.calls++
	f.action = action
	return f.failure
}
func TestGitHubMonitorTransitionWaitsUsesModalChildAndKeepsFailure(t *testing.T) {
	old := &modelTransitionFixture{}
	fresh := &modelTransitionFixture{}
	child := models.Issue{ID: "gh-2", Status: models.StatusInProgress}
	m := Model{DataSource: dashboardOnlyFixture{}, IssueTransitions: map[string]MonitorTransitionStore{"gh-2": fresh}, ModalStack: []ModalEntry{{IssueID: "gh-1", Issue: &models.Issue{ID: "gh-1", Type: models.TypeEpic}, TaskSectionFocused: true, EpicTasks: []models.Issue{child}, Transitions: map[string]MonitorTransitionStore{"gh-2": old}}}}
	result, cmd := m.markForReview()
	m = result.(Model)
	if cmd == nil || !m.WorkflowPending || m.CurrentModal().EpicTasks[0].Status != models.StatusInProgress || old.calls != 0 {
		t.Fatal("optimistic mutation")
	}
	if _, duplicate := m.reopenIssue(); duplicate != nil {
		t.Fatal("concurrent workflow")
	}
	msg := cmd().(MonitorTransitionedMsg)
	if old.calls != 1 || fresh.calls != 0 || old.action != "review" || msg.IssueID != "gh-2" {
		t.Fatal("used unrelated newer observation")
	}
	m.BoardEditorOpen = true
	m.BoardEditorMode = "edit"
	result, refresh := m.Update(msg)
	m = result.(Model)
	if refresh == nil || m.WorkflowPending || m.CurrentModal().EpicTasks[0].Status != models.StatusInProgress || m.CurrentModal().Transitions["gh-2"] != nil {
		t.Fatal("missing acknowledgment refresh")
	}
	if _, duplicate := m.Update(msg); duplicate != nil {
		t.Fatal("duplicate reply refreshed")
	}
	m.BoardEditorOpen = false
	m.ModalStack[0].Transitions = map[string]MonitorTransitionStore{"gh-2": old}
	old.failure = errors.New("write outcome unknown")
	result, cmd = m.reopenIssue()
	m = result.(Model)
	result, refresh = m.Update(cmd())
	m = result.(Model)
	if refresh != nil || !m.StatusIsError || m.WorkflowPending || m.CurrentModal() == nil || m.CurrentModal().Transitions["gh-2"] != nil {
		t.Fatal("failure lost or retry handle retained")
	}
	if _, retry := m.reopenIssue(); retry != nil {
		t.Fatal("stale retry started")
	}
}
func TestGitHubMonitorTransitionMissingObservationAndDeleteExclusion(t *testing.T) {
	m := Model{DataSource: dashboardOnlyFixture{}, ModalStack: []ModalEntry{{IssueID: "gh-1", Issue: &models.Issue{ID: "gh-1"}}}}
	result, cmd := m.markForReview()
	m = result.(Model)
	if cmd != nil || !m.StatusIsError {
		t.Fatal("missing observation used SQLite")
	}
	m.DeletePending = true
	if _, cmd = m.markForReview(); cmd != nil {
		t.Fatal("workflow overlapped delete")
	}
	m.DeletePending = false
	m.WorkflowPending = true
	if _, cmd = m.confirmDelete(); cmd != nil {
		t.Fatal("delete overlapped workflow")
	}
}

package monitor

import (
	"context"
	"errors"
	"testing"

	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
)

type formEditFixture struct {
	monitorDataFixture
	expected *ghstore.Record
	result   *ghstore.Record
	readErr  error
}

func (f *formEditFixture) ReadObserved(_ context.Context, observed *ghstore.Record) (*ghstore.Record, error) {
	f.expected = observed
	return f.result, f.readErr
}
func TestGitHubFormEditReadsOriginalObservationAndDependencies(t *testing.T) {
	original := ghstore.Record{Issue: models.Issue{ID: "gh-1", Title: "shown", Status: models.StatusOpen}, Details: &ghstore.IssueDetails{Dependencies: []string{"gh-2"}}}
	f := &formEditFixture{result: &original}
	s := &GitHubDataSource{ctx: context.Background(), open: func(context.Context) (GitHubMonitorReader, error) { return f, nil }}
	store := &githubIssueTransition{source: s, observed: original}
	issue, deps, err := store.PrepareEdit()
	if err != nil || issue.Title != "shown" || len(deps) != 1 || f.expected != &store.observed {
		t.Fatalf("%+v %v %v", issue, deps, err)
	}
	deps[0] = "gh-9"
	if original.Details.Dependencies[0] != "gh-2" {
		t.Fatal("dependencies aliased backend")
	}
	f.readErr = &ghstore.ConflictError{ID: "gh-1"}
	issue, deps, err = store.PrepareEdit()
	if err == nil || issue.ID != "" || deps != nil {
		t.Fatal("stale data returned")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.ctx = ctx
	if _, _, err := store.PrepareEdit(); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestGitHubFormEditUsesModalObservationAndPreservesDraft(t *testing.T) {
	original := ghstore.Record{Issue: models.Issue{ID: "gh-1", Title: "modal snapshot", Status: models.StatusOpen}, Details: &ghstore.IssueDetails{Dependencies: []string{"gh-2"}}}
	f := &formEditFixture{result: &original}
	s := &GitHubDataSource{ctx: context.Background(), open: func(context.Context) (GitHubMonitorReader, error) { return f, nil }}
	store := &githubIssueTransition{source: s, observed: original}
	m := Model{DataSource: s, Width: 100, Height: 40, ModalStack: []ModalEntry{{IssueID: "gh-1", Issue: &original.Issue, Transitions: map[string]MonitorTransitionStore{"gh-1": store}}}, IssueTransitions: map[string]MonitorTransitionStore{"gh-1": &githubIssueTransition{source: s, observed: ghstore.Record{Issue: models.Issue{ID: "gh-1", Title: "new dashboard"}}}}}
	pending, cmd := m.openEditIssueForm()
	m = pending.(Model)
	if cmd == nil || !m.WorkflowPending || m.FormOpen || m.PendingRemoteWrite() {
		t.Fatal("edit was not prepared asynchronously")
	}
	if _, cmd := m.openNewIssueForm(); cmd != nil {
		t.Fatal("new form raced edit preparation")
	}
	msg := cmd().(MonitorEditPreparedMsg)
	opened, _ := m.Update(msg)
	m = opened.(Model)
	if !m.FormOpen || m.WorkflowPending || m.FormState.Title != "modal snapshot" || m.FormState.Dependencies != "gh-2" || m.FormEditStore != store {
		t.Fatal("wrong snapshot or dependencies")
	}
	m.FormState.Title = "draft"
	repeated, _ := m.Update(msg)
	m = repeated.(Model)
	if m.FormState.Title != "draft" {
		t.Fatal("duplicate response erased draft")
	}
	m.closeForm()
	if m.FormEditStore != nil {
		t.Fatal("closed form retained store")
	}
	m.WorkflowPending = true
	m.WorkflowRequest++
	msg.Request = m.WorkflowRequest
	msg.Error = &ghstore.ConflictError{ID: "gh-1"}
	failed, _ := m.Update(msg)
	m = failed.(Model)
	if m.FormOpen || m.WorkflowPending || !m.StatusIsError {
		t.Fatal("stale observation opened form")
	}
}

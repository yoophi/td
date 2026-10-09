package monitor

import (
	"context"
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
)

func TestGitHubMonitorCloseUsesOriginalObservationAndReason(t *testing.T) {
	f := &transitionFixture{detailFixture: detailFixture{root: &ghstore.Record{Issue: models.Issue{ID: "gh-1", Title: "Original", Status: models.StatusOpen}}}}
	s := &GitHubDataSource{ctx: context.Background(), baseDir: t.TempDir(), actor: "actual-monitor", open: func(context.Context) (GitHubMonitorReader, error) { return f, nil }}
	msg := s.Details("gh-1")
	if msg.Error != nil {
		t.Fatal(msg.Error)
	}
	store := msg.Transitions["gh-1"].(MonitorCloseStore)
	f.root.Title = "Peer"
	var conflict *ghstore.ConflictError
	if err := store.Close("reason"); !errors.As(err, &conflict) || f.writes != 0 || f.original != "Original" {
		t.Fatalf("stale close %v", err)
	}
	msg = s.Details("gh-1")
	store = msg.Transitions["gh-1"].(MonitorCloseStore)
	if err := store.Close(" reviewed externally "); err != nil || f.writes != 1 || f.action != "close" || f.options.Reason != "reviewed externally" || f.options.SessionID != "actual-monitor" || f.options.SelfReview || f.options.ReviewedBy != "" {
		t.Fatalf("close options %+v %v", f.options, err)
	}
}

type modelCloseFixture struct {
	modelTransitionFixture
	issue  models.Issue
	reason string
}

func (f *modelCloseFixture) Close(reason string) error {
	f.calls++
	f.reason = reason
	return f.failure
}
func (f *modelCloseFixture) ObservedIssue() models.Issue { return f.issue }
func TestGitHubMonitorCloseConfirmationKeepsHandleAndTypedReason(t *testing.T) {
	original := &modelCloseFixture{issue: models.Issue{ID: "gh-1", Title: "Original", Status: models.StatusOpen}}
	peer := &modelCloseFixture{issue: models.Issue{ID: "gh-1", Title: "Peer", Status: models.StatusOpen}}
	m := Model{DataSource: dashboardOnlyFixture{}, ModalStack: []ModalEntry{{IssueID: "gh-1", Issue: &original.issue, Transitions: map[string]MonitorTransitionStore{"gh-1": original}}}}
	result, cmd := m.confirmClose()
	m = result.(Model)
	if cmd != nil || !m.CloseConfirmOpen || m.CloseStore != original {
		t.Fatal("missing frozen confirmation")
	}
	m.CloseConfirmModal.Render(80, 24, m.CloseConfirmMouseHandler)
	_, _ = m.CloseConfirmModal.HandleMsg(tea.PasteMsg{Content: " verified result "})
	m.ModalStack[0].Transitions = map[string]MonitorTransitionStore{"gh-1": peer}
	result, cmd = m.executeCloseWithReason()
	m = result.(Model)
	if cmd == nil || !m.WorkflowPending || !m.CloseConfirmOpen || original.calls != 0 || m.CurrentModal().Issue.Status != models.StatusOpen {
		t.Fatal("optimistic close")
	}
	if _, duplicate := m.executeCloseWithReason(); duplicate != nil {
		t.Fatal("duplicate close")
	}
	msg := cmd().(MonitorTransitionedMsg)
	if original.calls != 1 || peer.calls != 0 || original.reason != "verified result" {
		t.Fatalf("reason %q", original.reason)
	}
	result, refresh := m.Update(msg)
	m = result.(Model)
	if refresh == nil || m.WorkflowPending || m.CloseConfirmOpen || m.CloseStore != nil {
		t.Fatal("acknowledgment did not close confirmation")
	}
	if _, duplicate := m.Update(msg); duplicate != nil {
		t.Fatal("duplicate reply refreshed")
	}
}
func TestGitHubMonitorCloseFailureKeepsConfirmationAndPreventsRetry(t *testing.T) {
	f := &modelCloseFixture{modelTransitionFixture: modelTransitionFixture{failure: errors.New("review required")}, issue: models.Issue{ID: "gh-1", Status: models.StatusOpen}}
	m := Model{DataSource: dashboardOnlyFixture{}, ModalStack: []ModalEntry{{IssueID: "gh-1", Issue: &f.issue, Transitions: map[string]MonitorTransitionStore{"gh-1": f}}}}
	result, _ := m.confirmClose()
	m = result.(Model)
	result, cmd := m.executeCloseWithReason()
	m = result.(Model)
	result, refresh := m.Update(cmd())
	m = result.(Model)
	if refresh != nil || !m.StatusIsError || !m.CloseConfirmOpen || m.CloseStore != nil || m.WorkflowPending || m.CurrentModal().Issue.Status != models.StatusOpen {
		t.Fatal("policy error lost confirmation")
	}
	if _, retry := m.executeCloseWithReason(); retry != nil || f.calls != 1 {
		t.Fatal("stale close retry")
	}
	m.closeCloseConfirmModal()
	if m.CloseStore != nil {
		t.Fatal("cancel retained handle")
	}
}

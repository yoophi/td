package monitor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
)

type deleteFixture struct {
	detailFixture
	writes, gets               int
	observedTitle, deleteActor string
	deleteErr                  error
}

func (f *deleteFixture) Get(_ context.Context, _ string) (*ghstore.Record, error) {
	f.gets++
	return f.root, nil
}
func (f *deleteFixture) SetDeletedObserved(_ context.Context, r *ghstore.Record, deleted bool, actor, reason string) (*ghstore.Record, bool, error) {
	f.observedTitle = r.Title
	f.deleteActor = actor
	if !deleted || reason != "" {
		return nil, false, errors.New("unexpected deletion arguments")
	}
	if r.Title != f.root.Title {
		return nil, false, &ghstore.ConflictError{ID: r.ID}
	}
	f.writes++
	return r, false, f.deleteErr
}
func deletionSource(f *deleteFixture) *GitHubDataSource {
	return &GitHubDataSource{ctx: context.Background(), actor: "actual-monitor", open: func(context.Context) (GitHubMonitorReader, error) { return f, nil }}
}
func TestGitHubMonitorDeleteKeepsConfirmationObservationAndActor(t *testing.T) {
	f := &deleteFixture{detailFixture: detailFixture{root: &ghstore.Record{Issue: models.Issue{ID: "gh-1", Title: "Original"}}}}
	s := deletionSource(f)
	issue, store, err := s.PrepareDelete("gh-1")
	if err != nil || issue.Title != "Original" || f.writes != 0 {
		t.Fatalf("prepare %v", err)
	}
	f.root.Title = "Peer"
	var conflict *ghstore.ConflictError
	if err = store.Delete(); !errors.As(err, &conflict) || f.gets != 1 || f.writes != 0 || f.observedTitle != "Original" {
		t.Fatalf("replaced observation %v", err)
	}
	_, store, err = s.PrepareDelete("gh-1")
	if err != nil {
		t.Fatal(err)
	}
	f.deleteErr = errors.New("write outcome unknown")
	if err = store.Delete(); err == nil || f.writes != 1 || f.deleteActor != "actual-monitor" || f.gets != 2 {
		t.Fatalf("delete %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.ctx = ctx
	if err = store.Delete(); !errors.Is(err, context.Canceled) || f.writes != 1 {
		t.Fatal("cancelled deletion wrote")
	}
}
func TestGitHubMonitorDeleteValidationBeforeRead(t *testing.T) {
	s := &GitHubDataSource{ctx: context.Background(), actor: "actual", open: func(context.Context) (GitHubMonitorReader, error) {
		t.Fatal("invalid preparation opened store")
		return nil, nil
	}}
	if _, _, err := s.PrepareDelete("1"); err == nil {
		t.Fatal("alias accepted")
	}
	s.actor = " "
	if _, _, err := s.PrepareDelete("gh-1"); err == nil {
		t.Fatal("missing actor accepted")
	}
}
func TestGitHubMonitorDeleteWaitsForAcknowledgmentAndBlocksDuplicateRetry(t *testing.T) {
	f := &deleteFixture{detailFixture: detailFixture{root: &ghstore.Record{Issue: models.Issue{ID: "gh-1", Title: "Original"}}}}
	m := Model{DataSource: deletionSource(f), ModalStack: []ModalEntry{{IssueID: "gh-1", Issue: &f.root.Issue}}}
	result, cmd := m.confirmDelete()
	m = result.(Model)
	if cmd == nil || !m.DeletePreparing || m.ConfirmOpen {
		t.Fatal("missing preparation")
	}
	if _, duplicate := m.confirmDelete(); duplicate != nil {
		t.Fatal("duplicate preparation")
	}
	prepared := cmd().(MonitorDeletePreparedMsg)
	result, _ = m.Update(prepared)
	m = result.(Model)
	if !m.ConfirmOpen || m.DeletePreparing || m.DeleteStore == nil || f.writes != 0 {
		t.Fatal("missing confirmation")
	}
	result, cmd = m.executeDelete()
	m = result.(Model)
	if cmd == nil || !m.DeletePending || !m.ConfirmOpen || m.CurrentModal() == nil || f.writes != 0 {
		t.Fatal("optimistic deletion")
	}
	if _, duplicate := m.executeDelete(); duplicate != nil {
		t.Fatal("duplicate deletion")
	}
	deleted := cmd().(MonitorDeletedMsg)
	result, refresh := m.Update(deleted)
	m = result.(Model)
	if deleted.Error != nil || m.DeletePending || m.ConfirmOpen || m.CurrentModal() != nil || refresh == nil || f.writes != 1 {
		t.Fatal("missing acknowledged refresh")
	}
	if _, duplicate := m.Update(deleted); duplicate != nil {
		t.Fatal("duplicate reply refreshed")
	}
}
func TestGitHubMonitorDeleteFailureRetainsModalAndRequiresNewObservation(t *testing.T) {
	f := &deleteFixture{detailFixture: detailFixture{root: &ghstore.Record{Issue: models.Issue{ID: "gh-1", Title: "Original"}}}, deleteErr: errors.New("uncertain write")}
	m := Model{DataSource: deletionSource(f), ModalStack: []ModalEntry{{IssueID: "gh-1", Issue: &f.root.Issue}}}
	result, prepare := m.confirmDelete()
	m = result.(Model)
	result, _ = m.Update(prepare())
	m = result.(Model)
	result, write := m.executeDelete()
	m = result.(Model)
	// Completion must survive overlays that intercept ordinary data messages.
	m.BoardEditorOpen = true
	m.BoardEditorMode = "edit"
	result, refresh := m.Update(write())
	m = result.(Model)
	if refresh != nil || !m.StatusIsError || !strings.Contains(m.StatusMessage, "inspect GitHub") || !m.ConfirmOpen || m.CurrentModal() == nil || m.DeleteStore != nil || m.DeletePending {
		t.Fatal("failure hidden or display discarded")
	}
	result, retry := m.executeDelete()
	m = result.(Model)
	if retry != nil || f.writes != 1 || !m.StatusIsError {
		t.Fatal("automatic stale retry")
	}
	m.closeDeleteConfirmModal()
	if m.DeleteStore != nil {
		t.Fatal("cancel retained deletion handle")
	}
}

package monitor

import (
	"errors"
	"strings"
	"testing"

	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/pkg/monitor/keymap"
)

type clipboardSourceFixture struct {
	dashboardOnlyFixture
	result IssueDetailsMsg
	id     string
}

func (f *clipboardSourceFixture) Details(id string) IssueDetailsMsg { f.id = id; return f.result }
func TestGitHubClipboardReadsSelectedIssueAsynchronouslyAndHandlesFailure(t *testing.T) {
	f := &clipboardSourceFixture{result: IssueDetailsMsg{Issue: &models.Issue{ID: "gh-1", Title: "Epic title", Type: models.TypeEpic}, EpicTasks: []models.Issue{{ID: "gh-2", Title: "Child title"}}}}
	copied := ""
	copies := 0
	m := Model{DataSource: f, ActivePanel: PanelCurrentWork, CurrentWorkRows: []string{"gh-1"}, ClipboardFn: func(value string) error { copied = value; copies++; return nil }}
	pending, cmd := m.copyCurrentIssueToClipboard()
	m = pending.(Model)
	if cmd == nil || !m.ClipboardPending || copied != "" {
		t.Fatal("copy did not defer network read")
	}
	if _, duplicate := m.copyCurrentIssueToClipboard(); duplicate != nil {
		t.Fatal("duplicate read queued")
	}
	msg := cmd().(MonitorClipboardMsg)
	m.CurrentWorkRows = []string{"gh-9"} // The request retains the original selection.
	result, _ := m.Update(msg)
	m = result.(Model)
	if m.ClipboardPending || copies != 1 || f.id != "gh-1" || !strings.Contains(copied, "Epic title") || !strings.Contains(copied, "Child title") {
		t.Fatal("wrong clipboard snapshot")
	}
	m.Update(msg)
	if copies != 1 {
		t.Fatal("duplicate reply copied again")
	}
	m.ClipboardPending = true
	m.ClipboardRequest++
	msg.Request = m.ClipboardRequest
	msg.Details.Error = errors.New("permission denied")
	result, _ = m.Update(msg)
	m = result.(Model)
	if copies != 1 || !m.StatusIsError || !strings.Contains(m.StatusMessage, "permission denied") {
		t.Fatal("failed read copied partial data")
	}
}
func TestGitHubExtendedFormCommandUsesSourceAndPreservesGeneration(t *testing.T) {
	m := Model{DataSource: noAutofillSource{}, Width: 100, Height: 40}
	opened, _ := m.openNewIssueForm()
	m = opened.(Model)
	m.FormState.ShowExtended = false
	m.FormState.Title = "draft"
	old := m.FormAutofillRequest
	extended, cmd := m.executeCommand(keymap.CmdFormToggleExtend)
	m = extended.(Model)
	if cmd == nil || m.FormAutofillRequest != old+1 {
		t.Fatal("extended form did not own its reload")
	}
	msg := cmd().(AutofillResultMsg)
	updated, _ := m.handleFormUpdate(msg)
	m = updated.(Model)
	if !m.StatusIsError || m.FormState.Title != "draft" || msg.Request != m.FormAutofillRequest || msg.Error == nil {
		t.Fatal("missing GitHub reader used SQLite or lost draft")
	}
}

func TestGitHubClipboardLoadingModalReadsItsIssueWithoutSQLite(t *testing.T) {
	f := &clipboardSourceFixture{result: IssueDetailsMsg{Issue: &models.Issue{ID: "gh-7", Title: "modal task"}}}
	m := Model{DataSource: f, ModalStack: []ModalEntry{{IssueID: "gh-7"}}, ClipboardFn: func(string) error { return errors.New("clipboard denied") }}
	pending, cmd := m.copyCurrentIssueToClipboard()
	m = pending.(Model)
	if cmd == nil {
		t.Fatal("loading modal had no read")
	}
	updated, _ := m.Update(cmd())
	m = updated.(Model)
	if f.id != "gh-7" || !m.StatusIsError || !strings.Contains(m.StatusMessage, "clipboard denied") {
		t.Fatal("wrong modal ID or missing clipboard error")
	}
}

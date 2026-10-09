package monitor

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestGitHubFooterKeepsRequestStateVisibleBesideError(t *testing.T) {
	m := NewModel(nil, "actual", time.Minute, "test", t.TempDir())
	source := &GitHubDataSource{}
	m.DataSource = source
	m.Width = 80
	m.StatusMessage = "Error refreshing monitor: " + strings.Repeat("long API failure ", 12)
	m.StatusIsError = true
	source.refreshing.Store(true)
	for _, saving := range []bool{false, true} {
		m.BoardEditorPending = saving
		footer := m.renderFooter()
		lines := strings.Split(footer, "\n")
		if len(lines) != 3 || !strings.Contains(ansi.Strip(lines[1]), "Error refreshing monitor") {
			t.Fatal("error feedback missing", footer)
		}
		state := "GitHub loading (Ctrl+C: cancel)"
		if saving {
			state = "GitHub saving (Ctrl+C: cancel request)"
		}
		if !strings.Contains(ansi.Strip(lines[2]), state) {
			t.Fatal("request state clipped", footer)
		}
		for _, line := range lines {
			if ansi.StringWidth(line) > m.Width {
				t.Fatal("footer exceeded terminal width", footer)
			}
		}
	}
}

func TestGitHubCancelWorksInsideFormAndRetainsPendingWriteFact(t *testing.T) {
	m := NewModel(nil, "actual", time.Minute, "test", t.TempDir())
	m.DataSource = &GitHubDataSource{}
	m.Width, m.Height = 100, 40
	opened, _ := m.openNewIssueForm()
	m = opened.(Model)
	m.FormState.Title = "draft"
	m.WorkflowPending, m.WorkflowWriting = true, true
	result, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("form swallowed cancellation")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("cancellation did not quit")
	}
	m = result.(Model)
	if !m.PendingRemoteWrite() || m.FormState.Title != "draft" {
		t.Fatal("cancellation concealed pending write or changed draft")
	}
}

package monitor

import (
	"fmt"

	tea "charm.land/bubbletea/v2"
	"github.com/marcus/td/internal/models"
)

type MonitorClipboardMsg struct {
	Request uint64
	IssueID string
	Details IssueDetailsMsg
}

func (m Model) copyRemoteIssue() (tea.Model, tea.Cmd) {
	if m.ClipboardPending {
		return m, nil
	}
	id := m.SelectedIssueID(m.ActivePanel)
	if modal := m.CurrentModal(); modal != nil {
		id = modal.IssueID
	}
	if id == "" {
		return m, nil
	}
	source, ok := m.DataSource.(MonitorDetailSource)
	if !ok {
		m.StatusMessage = "GitHub monitor clipboard reader unavailable"
		m.StatusIsError = true
		return m, nil
	}
	m.ClipboardRequest++
	request := m.ClipboardRequest
	m.ClipboardPending = true
	return m, func() tea.Msg { return MonitorClipboardMsg{Request: request, IssueID: id, Details: source.Details(id)} }
}
func (m Model) handleRemoteClipboard(msg MonitorClipboardMsg) (tea.Model, tea.Cmd) {
	if !m.ClipboardPending || msg.Request != m.ClipboardRequest {
		return m, nil
	}
	m.ClipboardPending = false
	detail := msg.Details
	err := detail.Error
	if err == nil && (detail.Issue == nil || detail.Issue.ID != msg.IssueID || detail.Issue.DeletedAt != nil) {
		err = fmt.Errorf("clipboard task observation unavailable")
	}
	if err != nil {
		m.StatusMessage = "Copy failed: " + err.Error()
		m.StatusIsError = true
		return m, nil
	}
	markdown := formatIssueAsMarkdown(detail.Issue)
	if detail.Issue.Type == models.TypeEpic {
		markdown = formatEpicAsMarkdown(detail.Issue, detail.EpicTasks)
	}
	copyFn := m.ClipboardFn
	if copyFn == nil {
		copyFn = copyToClipboard
	}
	if err := copyFn(markdown); err != nil {
		m.StatusMessage = "Copy failed: " + err.Error()
		m.StatusIsError = true
	} else {
		m.StatusMessage = "Yanked to clipboard"
		m.StatusIsError = false
	}
	return m, nil
}

package monitor

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
)

type MonitorCloseStore interface {
	Close(string) error
	ObservedIssue() models.Issue
}

func (t *githubIssueTransition) ObservedIssue() models.Issue { return t.observed.Issue }
func (t *githubIssueTransition) Close(reason string) error {
	return t.applyTransition("close", ghstore.TransitionOptions{Reason: strings.TrimSpace(reason)})
}

func (m Model) confirmRemoteClose() (tea.Model, tea.Cmd) {
	if m.WorkflowPending || m.DeletePreparing || m.DeletePending {
		m.StatusMessage = "An issue change is still running"
		return m, nil
	}
	id := m.SelectedIssueID(m.ActivePanel)
	stores := m.IssueTransitions
	if modal := m.CurrentModal(); modal != nil && modal.Issue != nil {
		id = modal.IssueID
		stores = modal.Transitions
		if modal.TaskSectionFocused && modal.EpicTasksCursor >= 0 && modal.EpicTasksCursor < len(modal.EpicTasks) {
			id = modal.EpicTasks[modal.EpicTasksCursor].ID
		}
	}
	if id == "" {
		return m, nil
	}
	store, ok := stores[id].(MonitorCloseStore)
	if !ok {
		m.StatusMessage = "Close observation unavailable; refresh before retrying"
		m.StatusIsError = true
		return m, nil
	}
	issue := store.ObservedIssue()
	if issue.ID != id {
		m.StatusMessage = "Close observation does not match selection"
		m.StatusIsError = true
		return m, nil
	}
	if issue.Status == models.StatusClosed {
		return m, nil
	}
	m = m.openCloseConfirmModal(id, issue.Title)
	m.CloseStore = store
	return m, nil
}
func (m Model) executeRemoteClose() (tea.Model, tea.Cmd) {
	if m.WorkflowPending || m.DeletePreparing || m.DeletePending {
		return m, nil
	}
	if m.CloseStore == nil || m.CloseConfirmIssueID == "" {
		m.StatusMessage = "Close observation unavailable; reopen confirmation"
		m.StatusIsError = true
		return m, nil
	}
	reason := ""
	if m.CloseConfirmModal != nil {
		reason = strings.TrimSpace(m.CloseConfirmModal.InputValue("reason"))
	}
	store := m.CloseStore
	id := m.CloseConfirmIssueID
	if store.ObservedIssue().ID != id {
		m.StatusMessage = fmt.Sprintf("Close observation does not match %s", id)
		m.StatusIsError = true
		return m, nil
	}
	m.WorkflowRequest++
	request := m.WorkflowRequest
	m.WorkflowPending = true
	m.WorkflowWriting = true
	return m, func() tea.Msg {
		return MonitorTransitionedMsg{IssueID: id, Action: "close", Request: request, Error: store.Close(reason)}
	}
}

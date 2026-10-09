package monitor

import (
	"context"
	"fmt"
	"maps"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/marcus/td/internal/features"
	"github.com/marcus/td/internal/ghstore"
)

type MonitorTransitionStore interface{ Transition(string) error }
type githubTransitionWriter interface {
	TransitionObservedWithCascades(context.Context, *ghstore.Record, string, ghstore.TransitionOptions) (*ghstore.Record, bool, error)
}
type githubIssueTransition struct {
	source   *GitHubDataSource
	observed ghstore.Record
}

func (t *githubIssueTransition) Transition(action string) error {
	if action != "review" && action != "reopen" {
		return fmt.Errorf("unsupported monitor transition %q", action)
	}
	return t.applyTransition(action, ghstore.TransitionOptions{})
}
func (t *githubIssueTransition) applyTransition(action string, options ghstore.TransitionOptions) error {
	if err := t.source.ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(t.source.actor) == "" {
		return fmt.Errorf("monitor transition requires actual session")
	}
	mode, err := features.ResolveReviewPolicyMode(t.source.baseDir)
	if err != nil {
		return err
	}
	raw, err := t.source.open(t.source.ctx)
	if err != nil {
		return err
	}
	c, ok := raw.(githubTransitionWriter)
	if !ok {
		return fmt.Errorf("GitHub monitor workflow writer unavailable")
	}
	options.SessionID = t.source.actor
	options.AgentType = "monitor"
	options.Mode = mode
	if err := ghstore.ValidateReviewOptions(action, options); err != nil {
		return err
	}
	_, _, err = c.TransitionObservedWithCascades(t.source.ctx, &t.observed, action, options)
	return err
}
func (s *GitHubDataSource) transitionHandles(records map[string]ghstore.Record) map[string]MonitorTransitionStore {
	out := map[string]MonitorTransitionStore{}
	for id, r := range records {
		if r.DeletedAt == nil {
			out[id] = &githubIssueTransition{source: s, observed: r}
		}
	}
	return out
}

type MonitorTransitionedMsg struct {
	IssueID, Action string
	Request         uint64
	Error           error
}

func (m Model) transitionRemoteIssue(action string) (tea.Model, tea.Cmd) {
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
	store := stores[id]
	if store == nil {
		m.StatusMessage = "Workflow observation unavailable; refresh before retrying"
		m.StatusIsError = true
		return m, nil
	}
	m.WorkflowRequest++
	request := m.WorkflowRequest
	m.WorkflowPending = true
	m.WorkflowWriting = true
	return m, func() tea.Msg {
		return MonitorTransitionedMsg{IssueID: id, Action: action, Request: request, Error: store.Transition(action)}
	}
}
func (m Model) handleRemoteTransitioned(msg MonitorTransitionedMsg) (tea.Model, tea.Cmd) {
	if !m.WorkflowPending || msg.Request != m.WorkflowRequest {
		return m, nil
	}
	m.WorkflowPending = false
	m.WorkflowWriting = false
	if msg.Action == "record-review" {
		m.RecordStore = nil
	}
	if msg.Action == "close" {
		m.CloseStore = nil
	}
	if msg.Action == "approve" {
		m.ApproveStore = nil
	}
	m.IssueTransitions = maps.Clone(m.IssueTransitions)
	delete(m.IssueTransitions, msg.IssueID)
	for i := range m.ModalStack {
		m.ModalStack[i].Transitions = maps.Clone(m.ModalStack[i].Transitions)
		delete(m.ModalStack[i].Transitions, msg.IssueID)
	}
	if msg.Error != nil {
		m.StatusMessage = "Failed to " + msg.Action + ": " + msg.Error.Error() + "; inspect GitHub and refresh before retrying"
		m.StatusIsError = true
		return m, nil
	}
	if msg.Action == "close" && m.CloseConfirmIssueID == msg.IssueID {
		m.closeCloseConfirmModal()
	}
	if msg.Action == "approve" && m.SelfReviewConfirmIssueID == msg.IssueID {
		m.closeSelfReviewConfirmModal()
	}
	if msg.Action == "record-review" && m.RecordReviewIssueID == msg.IssueID {
		m.closeRecordReviewModal()
	}
	m.StatusMessage = msg.Action + " saved for " + msg.IssueID
	m.StatusIsError = false
	cmds := []tea.Cmd{m.fetchData()}
	if modal := m.CurrentModal(); modal != nil {
		cmds = append(cmds, m.fetchIssueDetails(modal.IssueID))
	}
	if m.TaskListMode == TaskListModeBoard && m.BoardMode.Board != nil {
		cmds = append(cmds, m.fetchBoardIssues(m.BoardMode.Board.ID))
	}
	return m, tea.Batch(cmds...)
}

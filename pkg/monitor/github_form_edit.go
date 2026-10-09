package monitor

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
)

type MonitorEditStore interface {
	ObservedIssue() models.Issue
	PrepareEdit() (models.Issue, []string, error)
}
type githubObservedReader interface {
	ReadObserved(context.Context, *ghstore.Record) (*ghstore.Record, error)
}

func (t *githubIssueTransition) PrepareEdit() (models.Issue, []string, error) {
	fail := func(err error) (models.Issue, []string, error) { return models.Issue{}, nil, err }
	if err := t.source.ctx.Err(); err != nil {
		return fail(err)
	}
	raw, err := t.source.open(t.source.ctx)
	if err != nil {
		return fail(err)
	}
	c, ok := raw.(githubObservedReader)
	if !ok {
		return fail(fmt.Errorf("GitHub monitor observed reader unavailable"))
	}
	current, err := c.ReadObserved(t.source.ctx, &t.observed)
	if err != nil {
		return fail(err)
	}
	if current == nil || current.ID != t.observed.ID || current.DeletedAt != nil {
		return fail(fmt.Errorf("edit observation unavailable"))
	}
	details, err := current.CopyDetails()
	if err != nil {
		return fail(err)
	}
	if err := t.source.ctx.Err(); err != nil {
		return fail(err)
	}
	return current.Issue, append([]string(nil), details.Dependencies...), nil
}

type MonitorEditPreparedMsg struct {
	Issue        models.Issue
	Dependencies []string
	Store        MonitorEditStore
	Request      uint64
	Error        error
}

func (m Model) prepareRemoteEdit() (tea.Model, tea.Cmd) {
	if m.WorkflowPending || m.DeletePreparing || m.DeletePending || m.FormOpen {
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
	store, ok := stores[id].(MonitorEditStore)
	if !ok || store.ObservedIssue().ID != id {
		m.StatusMessage = "Edit observation unavailable; refresh before retrying"
		m.StatusIsError = true
		return m, nil
	}
	m.WorkflowRequest++
	request := m.WorkflowRequest
	m.WorkflowPending = true
	return m, func() tea.Msg {
		issue, deps, err := store.PrepareEdit()
		if err == nil && issue.ID != id {
			err = fmt.Errorf("edit observation does not match selection")
		}
		return MonitorEditPreparedMsg{Issue: issue, Dependencies: deps, Store: store, Request: request, Error: err}
	}
}
func (m Model) handleRemoteEditPrepared(msg MonitorEditPreparedMsg) (tea.Model, tea.Cmd) {
	if !m.WorkflowPending || msg.Request != m.WorkflowRequest {
		return m, nil
	}
	m.WorkflowPending = false
	if msg.Error != nil {
		m.StatusMessage = "Cannot edit: " + msg.Error.Error() + "; refresh before retrying"
		m.StatusIsError = true
		return m, nil
	}
	if m.FormOpen {
		return m, nil
	}
	if msg.Store == nil || msg.Issue.ID == "" || msg.Store.ObservedIssue().ID != msg.Issue.ID || msg.Issue.DeletedAt != nil {
		m.StatusMessage = "Edit observation unavailable"
		m.StatusIsError = true
		return m, nil
	}
	m.FormState = newFormStateForEditWithTheme(&msg.Issue, m.themeOrDefault())
	m.FormState.Dependencies = strings.Join(msg.Dependencies, ", ")
	m.FormState.buildForm()
	m.FormOpen = true
	m.FormScrollOffset = 0
	m.FormEditStore = msg.Store
	width, _ := m.formModalDimensions()
	m.FormState.Width = modalInnerWidth(width)
	m.FormState.Form.WithWidth(m.FormState.Width)
	autofill := m.loadFormAutofill()
	return m, tea.Batch(m.FormState.Form.Init(), autofill)
}

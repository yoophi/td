package monitor

import (
	"context"
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/marcus/td/internal/features"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/workflow"
)

type MonitorEditWriter interface {
	SaveEdit(models.Issue, []string) error
}
type githubFormWriter interface {
	githubObservedReader
	UpdateWorkflowObserved(context.Context, *ghstore.Record, ghstore.Changes, ghstore.TransitionOptions) (*ghstore.Record, error)
	ChangeDependencyObserved(context.Context, *ghstore.Record, string, bool, string) (*ghstore.Record, error)
}

func (t *githubIssueTransition) SaveEdit(issue models.Issue, deps []string) error {
	if err := t.source.ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(t.source.actor) == "" {
		return fmt.Errorf("form update requires actual session")
	}
	if issue.ID != t.observed.ID {
		return fmt.Errorf("form issue does not match original observation")
	}
	if strings.TrimSpace(issue.Title) == "" {
		return fmt.Errorf("title is required")
	}
	if issue.Status != t.observed.Status && !workflow.DefaultMachine().IsValidTransition(t.observed.Status, issue.Status) {
		return fmt.Errorf("invalid status transition from %s to %s", t.observed.Status, issue.Status)
	}
	desired := []string{}
	for _, id := range deps {
		n, err := ghstore.Number(id)
		if err != nil {
			return err
		}
		canonical := fmt.Sprintf("gh-%d", n)
		if canonical == issue.ID {
			return fmt.Errorf("cannot depend on self")
		}
		if slices.Contains(desired, canonical) {
			return fmt.Errorf("duplicate dependency %s", canonical)
		}
		desired = append(desired, canonical)
	}
	mode, err := features.ResolveReviewPolicyMode(t.source.baseDir)
	if err != nil {
		return err
	}
	raw, err := t.source.open(t.source.ctx)
	if err != nil {
		return err
	}
	c, ok := raw.(githubFormWriter)
	if !ok {
		return fmt.Errorf("GitHub monitor form writer unavailable")
	}
	// Even a no-op submission must reject a stale form.
	root, err := c.ReadObserved(t.source.ctx, &t.observed)
	if err != nil {
		return err
	}
	if root == nil || root.ID != issue.ID {
		return fmt.Errorf("form observation unavailable")
	}
	old := root.Issue
	change := ghstore.Changes{}
	if issue.Title != old.Title {
		change.Title = &issue.Title
	}
	if issue.Description != old.Description {
		change.Description = &issue.Description
	}
	if issue.Acceptance != old.Acceptance {
		change.Acceptance = &issue.Acceptance
	}
	if issue.Type != old.Type {
		change.Type = &issue.Type
	}
	if issue.Priority != old.Priority {
		change.Priority = &issue.Priority
	}
	if issue.Points != old.Points {
		change.Points = &issue.Points
	}
	if !slices.Equal(issue.Labels, old.Labels) {
		change.Labels = &issue.Labels
	}
	if issue.ParentID != old.ParentID {
		change.ParentID = &issue.ParentID
	}
	if issue.Minor != old.Minor {
		change.Minor = &issue.Minor
	}
	options := ghstore.TransitionOptions{SessionID: t.source.actor, AgentType: "monitor", Mode: mode}
	saved := false
	fail := func(err error) error {
		if saved {
			return fmt.Errorf("%s some form changes were saved; inspect GitHub and reopen the form before retrying: %w", issue.ID, err)
		}
		return err
	}
	if change.HasFields() {
		root, err = c.UpdateWorkflowObserved(t.source.ctx, root, change, options)
		if err != nil {
			return err
		}
		saved = true
	}
	details, err := root.CopyDetails()
	if err != nil {
		return fail(err)
	}
	// Remove first so the intended graph does not temporarily retain old edges.
	for _, id := range details.Dependencies {
		if !slices.Contains(desired, id) {
			root, err = c.ChangeDependencyObserved(t.source.ctx, root, id, false, t.source.actor)
			if err != nil {
				return fail(err)
			}
			saved = true
		}
	}
	for _, id := range desired {
		if !slices.Contains(details.Dependencies, id) {
			root, err = c.ChangeDependencyObserved(t.source.ctx, root, id, true, t.source.actor)
			if err != nil {
				return fail(err)
			}
			saved = true
		}
	}
	if issue.Status != old.Status {
		_, err = c.UpdateWorkflowObserved(t.source.ctx, root, ghstore.Changes{Status: &issue.Status}, options)
		if err != nil {
			return fail(err)
		}
	}
	return nil
}

type MonitorFormSavedMsg struct {
	IssueID              string
	Request, FormRequest uint64
	Error                error
	Created              bool
}

func (m Model) submitRemoteForm() (tea.Model, tea.Cmd) {
	if m.WorkflowPending || m.DeletePreparing || m.DeletePending {
		return m, nil
	}
	if m.FormState.Mode == FormModeCreate {
		return m.submitRemoteCreate()
	}
	if m.FormState.Mode != FormModeEdit {
		return m, nil
	}
	store, ok := m.FormEditStore.(MonitorEditWriter)
	if !ok || m.FormEditStore.ObservedIssue().ID != m.FormState.IssueID {
		m.StatusMessage = "Form observation unavailable; inspect GitHub and reopen the form"
		m.StatusIsError = true
		return m, nil
	}
	issue := *m.FormState.ToIssue()
	issue.ID = m.FormState.IssueID
	deps := m.FormState.GetDependencies()
	m.WorkflowRequest++
	m.WorkflowPending = true
	request, formRequest := m.WorkflowRequest, m.FormAutofillRequest
	return m, func() tea.Msg {
		return MonitorFormSavedMsg{IssueID: issue.ID, Request: request, FormRequest: formRequest, Error: store.SaveEdit(issue, deps)}
	}
}
func (m Model) handleRemoteFormSaved(msg MonitorFormSavedMsg) (tea.Model, tea.Cmd) {
	if !m.WorkflowPending || msg.Request != m.WorkflowRequest {
		return m, nil
	}
	// A failed or uncertain write must not be repeated with the same form handle.
	m.FormEditStore = nil
	if msg.Error != nil && m.FormOpen && m.FormAutofillRequest == msg.FormRequest {
		m.FormSaveError = msg.Error
	}
	if msg.Error == nil && m.FormOpen && m.FormAutofillRequest == msg.FormRequest {
		m.closeForm()
	}
	action := "update"
	if msg.Created {
		action = "create"
	}
	return m.handleRemoteTransitioned(MonitorTransitionedMsg{IssueID: msg.IssueID, Request: msg.Request, Action: action, Error: msg.Error})
}

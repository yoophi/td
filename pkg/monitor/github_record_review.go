package monitor

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/reviewpolicy"
)

type MonitorRecordReviewStore interface {
	ObservedIssue() models.Issue
	RecordPlan() (MonitorApprovalPlan, error)
	RecordReview(string, string, string, bool) error
}

func (t *githubIssueTransition) RecordPlan() (MonitorApprovalPlan, error) {
	return t.approvalPlan(true)
}
func (t *githubIssueTransition) RecordReview(decision, reviewedBy, reason string, self bool) error {
	return t.applyTransition("approve", ghstore.TransitionOptions{RecordOnly: true, Decision: decision, ReviewedBy: strings.TrimSpace(reviewedBy), Reason: strings.TrimSpace(reason), SelfReview: self})
}

type MonitorRecordReviewPreparedMsg struct {
	IssueID string
	Request uint64
	Store   MonitorRecordReviewStore
	Plan    MonitorApprovalPlan
	Error   error
}

func (m Model) prepareRemoteRecordReview() (tea.Model, tea.Cmd) {
	if m.WorkflowPending || m.DeletePreparing || m.DeletePending {
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
	store, ok := stores[id].(MonitorRecordReviewStore)
	if !ok || store.ObservedIssue().ID != id {
		m.StatusMessage = "Record-review observation unavailable; refresh before retrying"
		m.StatusIsError = true
		return m, nil
	}
	m.WorkflowRequest++
	request := m.WorkflowRequest
	m.WorkflowPending = true
	return m, func() tea.Msg {
		plan, err := store.RecordPlan()
		return MonitorRecordReviewPreparedMsg{IssueID: id, Request: request, Store: store, Plan: plan, Error: err}
	}
}
func (m Model) handleRemoteRecordPrepared(msg MonitorRecordReviewPreparedMsg) (tea.Model, tea.Cmd) {
	if !m.WorkflowPending || msg.Request != m.WorkflowRequest {
		return m, nil
	}
	m.WorkflowPending = false
	if msg.Error != nil {
		m.StatusMessage = "Cannot record review: " + msg.Error.Error()
		m.StatusIsError = true
		return m, nil
	}
	if msg.Store == nil || msg.Store.ObservedIssue().ID != msg.IssueID || msg.Plan.CloseExisting {
		m.StatusMessage = "Record-review observation unavailable"
		m.StatusIsError = true
		return m, nil
	}
	issue := msg.Store.ObservedIssue()
	m.RecordStore = msg.Store
	m.RecordSelfReviewPrompt = msg.Plan.SelfReviewPrompt
	m = m.openRecordReviewModal(issue.ID, issue.Title)
	return m, nil
}
func (m Model) executeRemoteRecordReview() (tea.Model, tea.Cmd) {
	if m.WorkflowPending || m.DeletePreparing || m.DeletePending {
		return m, nil
	}
	id := m.RecordReviewIssueID
	store := m.RecordStore
	if store == nil || id == "" || store.ObservedIssue().ID != id {
		m.StatusMessage = "Record-review observation unavailable; reopen confirmation"
		m.StatusIsError = true
		return m, nil
	}
	reason, by := "", ""
	if m.RecordReviewModal != nil {
		reason = strings.TrimSpace(m.RecordReviewModal.InputValue("reason"))
		by = strings.TrimSpace(m.RecordReviewModal.InputValue("reviewed_by"))
	}
	if reason == "" {
		m.StatusMessage = "record-review requires a reason"
		m.StatusIsError = true
		return m, nil
	}
	decision := m.RecordReviewDecision
	if decision == "" {
		decision = reviewpolicy.DecisionApproved
	}
	if decision != reviewpolicy.DecisionApproved && decision != reviewpolicy.DecisionChangesRequested {
		m.StatusMessage = fmt.Sprintf("invalid record-review decision %q", decision)
		m.StatusIsError = true
		return m, nil
	}
	self := m.RecordSelfReviewPrompt && by == ""
	m.WorkflowRequest++
	request := m.WorkflowRequest
	m.WorkflowPending = true
	return m, func() tea.Msg {
		return MonitorTransitionedMsg{IssueID: id, Action: "record-review", Request: request, Error: store.RecordReview(decision, by, reason, self)}
	}
}

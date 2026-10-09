package monitor

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/marcus/td/internal/features"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/reviewpolicy"
)

type MonitorApprovalPlan struct{ CloseExisting, NeedInput, SelfReviewPrompt bool }
type MonitorApproveStore interface {
	ObservedIssue() models.Issue
	ApprovalPlan() (MonitorApprovalPlan, error)
	Approve(string, string, bool) error
}

func (t *githubIssueTransition) ApprovalPlan() (MonitorApprovalPlan, error) {
	return t.approvalPlan(false)
}
func (t *githubIssueTransition) approvalPlan(recordOnly bool) (MonitorApprovalPlan, error) {
	fail := func(err error) (MonitorApprovalPlan, error) { return MonitorApprovalPlan{}, err }
	if err := t.source.ctx.Err(); err != nil {
		return fail(err)
	}
	if strings.TrimSpace(t.source.actor) == "" {
		return fail(fmt.Errorf("approval requires actual session"))
	}
	if t.observed.Status != models.StatusInReview {
		return fail(fmt.Errorf("approval requires in_review status"))
	}
	mode, err := features.ResolveReviewPolicyMode(t.source.baseDir)
	if err != nil {
		return fail(err)
	}
	if recordOnly && mode != reviewpolicy.ModeTrusted && mode != reviewpolicy.ModeDelegated {
		return fail(fmt.Errorf("record-review requires trusted or delegated policy"))
	}
	c, err := t.source.open(t.source.ctx)
	if err != nil {
		return fail(err)
	}
	facts, err := c.ObserveMonitorReview(t.source.ctx, &t.observed, t.source.actor)
	if err != nil {
		return fail(err)
	}
	if facts == nil || !facts.Fresh {
		return fail(fmt.Errorf("review observation is stale; refresh before approving"))
	}
	if facts.ActiveApproval && (mode == reviewpolicy.ModeTrusted || mode == reviewpolicy.ModeDelegated) {
		if recordOnly {
			return fail(fmt.Errorf("review already recorded; close on the existing approval"))
		}
		return MonitorApprovalPlan{CloseExisting: true}, nil
	}
	in := monitorApproveInputs{Mode: mode, Issue: &t.observed.Issue, SessionID: t.source.actor, HasImplementationHistory: facts.ImplementationInvolved, WasAnyInvolved: facts.AnyInvolved, HasActiveApproval: facts.ActiveApproval}
	decision := monitorApproveDecision(in)
	if decision.Allowed {
		return MonitorApprovalPlan{NeedInput: decision.RequiresReason}, nil
	}
	if mode == reviewpolicy.ModeTrusted {
		in.SelfReviewAcknowledged = true
		acknowledged := monitorApproveDecision(in)
		if acknowledged.Allowed && acknowledged.SelfReview {
			return MonitorApprovalPlan{NeedInput: true, SelfReviewPrompt: true}, nil
		}
	}
	if recordOnly {
		in.AttributedTo = "reviewer"
		in.SelfReviewAcknowledged = false
		if monitorApproveDecision(in).Allowed {
			return MonitorApprovalPlan{NeedInput: true}, nil
		}
	}
	return fail(fmt.Errorf("approval unavailable: %s", decision.RejectionMessage))
}
func (t *githubIssueTransition) Approve(reviewedBy, reason string, self bool) error {
	return t.applyTransition("approve", ghstore.TransitionOptions{ReviewedBy: strings.TrimSpace(reviewedBy), Reason: strings.TrimSpace(reason), SelfReview: self})
}

type MonitorApprovalPreparedMsg struct {
	IssueID string
	Request uint64
	Store   MonitorApproveStore
	Plan    MonitorApprovalPlan
	Error   error
}

func (m Model) prepareRemoteApprove() (tea.Model, tea.Cmd) {
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
	store, ok := stores[id].(MonitorApproveStore)
	if !ok || store.ObservedIssue().ID != id {
		m.StatusMessage = "Approval observation unavailable; refresh before retrying"
		m.StatusIsError = true
		return m, nil
	}
	m.WorkflowRequest++
	request := m.WorkflowRequest
	m.WorkflowPending = true
	return m, func() tea.Msg {
		plan, err := store.ApprovalPlan()
		return MonitorApprovalPreparedMsg{IssueID: id, Request: request, Store: store, Plan: plan, Error: err}
	}
}
func (m Model) handleRemoteApprovalPrepared(msg MonitorApprovalPreparedMsg) (tea.Model, tea.Cmd) {
	if !m.WorkflowPending || msg.Request != m.WorkflowRequest {
		return m, nil
	}
	m.WorkflowPending = false
	if msg.Error != nil {
		m.StatusMessage = "Cannot approve: " + msg.Error.Error()
		m.StatusIsError = true
		return m, nil
	}
	if msg.Store == nil || msg.Store.ObservedIssue().ID != msg.IssueID {
		m.StatusMessage = "Approval observation unavailable"
		m.StatusIsError = true
		return m, nil
	}
	issue := msg.Store.ObservedIssue()
	if msg.Plan.CloseExisting {
		store, ok := msg.Store.(MonitorCloseStore)
		if !ok {
			m.StatusMessage = "Close-after-review writer unavailable"
			m.StatusIsError = true
			return m, nil
		}
		m = m.openCloseConfirmModal(issue.ID, issue.Title)
		m.CloseStore = store
		return m, nil
	}
	if msg.Plan.NeedInput {
		m.ApproveStore = msg.Store
		m.ApprovalSelfReviewPrompt = msg.Plan.SelfReviewPrompt
		m = m.openSelfReviewConfirmModal(issue.ID, issue.Title)
		return m, nil
	}
	return m.startRemoteApprove(msg.Store, msg.IssueID, "", "", false)
}
func (m Model) startRemoteApprove(store MonitorApproveStore, id, reviewedBy, reason string, self bool) (tea.Model, tea.Cmd) {
	if m.WorkflowPending || m.DeletePreparing || m.DeletePending {
		return m, nil
	}
	m.WorkflowRequest++
	request := m.WorkflowRequest
	m.WorkflowPending = true
	return m, func() tea.Msg {
		return MonitorTransitionedMsg{IssueID: id, Action: "approve", Request: request, Error: store.Approve(reviewedBy, reason, self)}
	}
}
func (m Model) executeRemoteApproveConfirmation() (tea.Model, tea.Cmd) {
	if m.WorkflowPending {
		return m, nil
	}
	id := m.SelfReviewConfirmIssueID
	store := m.ApproveStore
	if store == nil || id == "" || store.ObservedIssue().ID != id {
		m.StatusMessage = "Approval observation unavailable; reopen confirmation"
		m.StatusIsError = true
		return m, nil
	}
	reviewedBy, reason := "", ""
	if m.SelfReviewConfirmModal != nil {
		reviewedBy = strings.TrimSpace(m.SelfReviewConfirmModal.InputValue("reviewed_by"))
		reason = strings.TrimSpace(m.SelfReviewConfirmModal.InputValue("reason"))
	}
	if reason == "" && reviewedBy == "" {
		m.StatusMessage = "Approval confirmation requires a review reason or reviewer"
		m.StatusIsError = true
		return m, nil
	}
	self := m.ApprovalSelfReviewPrompt && reviewedBy == ""
	return m.startRemoteApprove(store, id, reviewedBy, reason, self)
}

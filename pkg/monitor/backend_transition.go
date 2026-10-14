package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/internal/models"
)

type backendWorkflowConnection interface {
	Detail(context.Context, string) (backendstore.Detail, error)
	RunIntent(context.Context, any, func() (backendstore.MutationRequest, error), any, func() error) error
}

type backendIssueTransition struct {
	source   *BackendDataSource
	writer   backendWorkflowConnection
	observed backendstore.Issue
}

func (s *BackendDataSource) backendTransitionHandles(snapshot backendstore.Monitor) map[string]MonitorTransitionStore {
	m := snapshot.Dashboard
	rows := make([]backendstore.Issue, 0, len(m.InProgress)+len(m.TaskList.Ready))
	if m.FocusedIssue != nil {
		rows = append(rows, *m.FocusedIssue)
	}
	rows = append(rows, m.InProgress...)
	for _, group := range [][]backendstore.Issue{m.TaskList.Reviewable, m.TaskList.NeedsRework, m.TaskList.InProgress, m.TaskList.Ready, m.TaskList.PendingReview, m.TaskList.ReadyToClose, m.TaskList.PendingOther, m.TaskList.Blocked, m.TaskList.Closed} {
		rows = append(rows, group...)
	}
	return s.backendTransitionHandlesForRows(rows)
}

func (s *BackendDataSource) backendTransitionHandlesForRows(rows []backendstore.Issue) map[string]MonitorTransitionStore {
	out := map[string]MonitorTransitionStore{}
	writer, ok := s.reader.(backendWorkflowConnection)
	if !ok {
		return out
	}
	for _, row := range rows {
		if row.DeletedAt == nil {
			out[row.ID] = &backendIssueTransition{source: s, writer: writer, observed: row}
		}
	}
	return out
}

func (t *backendIssueTransition) Transition(action string) error {
	if action != "review" && action != "reopen" {
		return fmt.Errorf("unsupported backend monitor transition %q", action)
	}
	if !backendstore.ValidIssueID(t.observed.ID) {
		return fmt.Errorf("invalid observed backend issue ID")
	}
	target := models.StatusOpen
	if action == "review" {
		target = models.StatusInReview
	}
	var reply struct {
		Issue backendstore.Issue `json:"issue"`
	}
	input := struct{ Action, Issue, Revision string }{action, t.observed.ID, t.observed.Revision}
	return t.writer.RunIntent(t.source.ctx, input, func() (backendstore.MutationRequest, error) {
		request := backendstore.MutationRequest{}
		if t.source.guard == nil {
			return request, fmt.Errorf("backend monitor context unavailable")
		}
		if err := t.source.guard(t.source.ctx); err != nil {
			return request, err
		}
		current, err := t.writer.Detail(t.source.ctx, t.observed.ID)
		if err != nil {
			return request, err
		}
		if err := t.source.guard(t.source.ctx); err != nil {
			return request, err
		}
		if current.Issue.Revision != t.observed.Revision || current.Issue.DeletedAt != nil {
			return request, fmt.Errorf("issue changed since it was displayed; refresh before workflow change")
		}
		return backendstore.MutationRequest{Method: "POST", Resource: "/issues/" + t.observed.ID + "/" + action, Revision: t.observed.Revision, Body: []byte("{}")}, nil
	}, &reply, func() error {
		if err := backendstore.ValidateIssue(reply.Issue); err != nil {
			return err
		}
		if reply.Issue.ID != t.observed.ID || reply.Issue.Status != target {
			return fmt.Errorf("backend workflow response differs from request")
		}
		return nil
	})
}

func (t *backendIssueTransition) ObservedIssue() models.Issue { return t.observed.Issue }

func (t *backendIssueTransition) ApprovalPlan() (MonitorApprovalPlan, error) {
	observed, err := t.approvalObservation()
	if err != nil {
		return MonitorApprovalPlan{}, err
	}
	if observed.Status != models.StatusInReview {
		return MonitorApprovalPlan{}, fmt.Errorf("approval requires in_review status")
	}
	if !slices.Contains(observed.AvailableTransitions, "approve") {
		return MonitorApprovalPlan{}, fmt.Errorf("approval unavailable for this session; refresh the monitor")
	}
	if len(observed.ActiveReview) > 0 && string(observed.ActiveReview) != "null" {
		return MonitorApprovalPlan{CloseExisting: true}, nil
	}
	return MonitorApprovalPlan{NeedInput: true, SelfReviewPrompt: t.actor() == observed.ImplementerSession}, nil
}

func (t *backendIssueTransition) RecordPlan() (MonitorApprovalPlan, error) {
	observed, err := t.approvalObservation()
	if err != nil {
		return MonitorApprovalPlan{}, err
	}
	if observed.Status != models.StatusInReview {
		return MonitorApprovalPlan{}, fmt.Errorf("record-review requires in_review status")
	}
	if len(observed.ActiveReview) > 0 && string(observed.ActiveReview) != "null" {
		return MonitorApprovalPlan{}, fmt.Errorf("review already recorded; close on the existing approval")
	}
	if !slices.Contains(observed.AvailableTransitions, "approve") {
		return MonitorApprovalPlan{}, fmt.Errorf("review unavailable for this session; refresh the monitor")
	}
	return MonitorApprovalPlan{NeedInput: true, SelfReviewPrompt: t.actor() == observed.ImplementerSession}, nil
}

func (t *backendIssueTransition) approvalObservation() (backendstore.Issue, error) {
	if t.source.guard == nil {
		return backendstore.Issue{}, fmt.Errorf("backend monitor context unavailable")
	}
	if err := t.source.guard(t.source.ctx); err != nil {
		return backendstore.Issue{}, err
	}
	snapshot, err := t.source.reader.Monitor(t.source.ctx, "", true, "priority")
	if err != nil {
		return backendstore.Issue{}, err
	}
	if err := t.source.guard(t.source.ctx); err != nil {
		return backendstore.Issue{}, err
	}
	for _, handle := range t.source.backendTransitionHandles(snapshot) {
		if other, ok := handle.(*backendIssueTransition); ok && other.observed.ID == t.observed.ID {
			if other.observed.Revision != t.observed.Revision {
				return backendstore.Issue{}, fmt.Errorf("issue changed since it was displayed; refresh before approval")
			}
			return other.observed, nil
		}
	}
	return backendstore.Issue{}, fmt.Errorf("approval observation unavailable")
}

func (t *backendIssueTransition) actor() string {
	if c, ok := t.source.reader.(*backendstore.Connection); ok {
		return c.SessionID
	}
	return ""
}

func (t *backendIssueTransition) Approve(reviewedBy, reason string, self bool) error {
	reviewedBy, reason = strings.TrimSpace(reviewedBy), strings.TrimSpace(reason)
	if self && reviewedBy != "" {
		return fmt.Errorf("self-review and named reviewer are mutually exclusive")
	}
	return t.runWorkflow("approve", "/approve", map[string]any{"reviewed_by": reviewedBy, "reason": reason, "self_review": self}, models.StatusClosed)
}

func (t *backendIssueTransition) Close(reason string) error {
	return t.runWorkflow("close", "/close", map[string]any{"reason": strings.TrimSpace(reason)}, models.StatusClosed)
}

func (t *backendIssueTransition) RecordReview(decision, reviewedBy, reason string, self bool) error {
	decision, reviewedBy, reason = strings.TrimSpace(decision), strings.TrimSpace(reviewedBy), strings.TrimSpace(reason)
	if decision != "approved" && decision != "changes_requested" || reason == "" || self && reviewedBy != "" {
		return fmt.Errorf("invalid backend review decision or attribution")
	}
	return t.runWorkflow("record-review", "/reviews", map[string]any{"decision": decision, "reviewed_by": reviewedBy, "summary": reason, "self_review": self}, models.StatusInReview)
}

func (t *backendIssueTransition) runWorkflow(action, suffix string, payload any, target models.Status) error {
	if !backendstore.ValidIssueID(t.observed.ID) {
		return fmt.Errorf("invalid observed backend issue ID")
	}
	var reply struct {
		Issue backendstore.Issue `json:"issue"`
	}
	input := struct {
		Action, Issue, Revision string
		Body                    any
	}{action, t.observed.ID, t.observed.Revision, payload}
	return t.writer.RunIntent(t.source.ctx, input, func() (backendstore.MutationRequest, error) {
		request := backendstore.MutationRequest{}
		if t.source.guard == nil {
			return request, fmt.Errorf("backend monitor context unavailable")
		}
		if err := t.source.guard(t.source.ctx); err != nil {
			return request, err
		}
		current, err := t.writer.Detail(t.source.ctx, t.observed.ID)
		if err != nil {
			return request, err
		}
		if err := t.source.guard(t.source.ctx); err != nil {
			return request, err
		}
		if current.Issue.Revision != t.observed.Revision || current.Issue.DeletedAt != nil {
			return request, fmt.Errorf("issue changed since it was displayed; refresh before workflow change")
		}
		body, err := json.Marshal(payload)
		return backendstore.MutationRequest{Method: "POST", Resource: "/issues/" + t.observed.ID + suffix, Revision: t.observed.Revision, Body: body}, err
	}, &reply, func() error {
		if err := backendstore.ValidateIssue(reply.Issue); err != nil {
			return err
		}
		if reply.Issue.ID != t.observed.ID || reply.Issue.Status != target {
			return fmt.Errorf("backend workflow response differs from request")
		}
		return nil
	})
}

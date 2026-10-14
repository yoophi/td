package monitor

import (
	"context"
	"fmt"

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

package monitor

import (
	"context"
	"fmt"

	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/internal/models"
)

type backendDeleteWriter interface {
	Detail(context.Context, string) (backendstore.Detail, error)
	RunIntent(context.Context, any, func() (backendstore.MutationRequest, error), any, func() error) error
}

type backendIssueDelete struct {
	source   *BackendDataSource
	writer   backendDeleteWriter
	observed backendstore.Issue
}

func (s *BackendDataSource) PrepareDelete(id string) (*models.Issue, MonitorDeleteStore, error) {
	if !backendstore.ValidIssueID(id) {
		return nil, nil, fmt.Errorf("monitor deletion requires a canonical backend issue ID")
	}
	if s.guard == nil {
		return nil, nil, fmt.Errorf("backend monitor context unavailable")
	}
	if err := s.guard(s.ctx); err != nil {
		return nil, nil, err
	}
	writer, ok := s.reader.(backendDeleteWriter)
	if !ok {
		return nil, nil, fmt.Errorf("backend deletion writer unavailable")
	}
	detail, err := writer.Detail(s.ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if err := s.guard(s.ctx); err != nil {
		return nil, nil, err
	}
	if detail.Issue.ID != id || detail.Issue.DeletedAt != nil {
		return nil, nil, fmt.Errorf("deletion target unavailable or deleted")
	}
	issue := detail.Issue.Issue
	return &issue, &backendIssueDelete{source: s, writer: writer, observed: detail.Issue}, nil
}

func (d *backendIssueDelete) Delete() error {
	var reply struct {
		Deleted bool `json:"deleted"`
	}
	input := struct{ Action, Issue, Revision string }{"issue.delete", d.observed.ID, d.observed.Revision}
	return d.writer.RunIntent(d.source.ctx, input, func() (backendstore.MutationRequest, error) {
		request := backendstore.MutationRequest{}
		if err := d.source.guard(d.source.ctx); err != nil {
			return request, err
		}
		current, err := d.writer.Detail(d.source.ctx, d.observed.ID)
		if err != nil {
			return request, err
		}
		if err := d.source.guard(d.source.ctx); err != nil {
			return request, err
		}
		if current.Issue.Revision != d.observed.Revision || current.Issue.DeletedAt != nil {
			return request, fmt.Errorf("issue changed since delete confirmation; refresh before deleting")
		}
		return backendstore.MutationRequest{Method: "DELETE", Resource: "/issues/" + d.observed.ID, Revision: d.observed.Revision}, nil
	}, &reply, func() error {
		if !reply.Deleted {
			return fmt.Errorf("backend did not confirm issue deletion")
		}
		return nil
	})
}

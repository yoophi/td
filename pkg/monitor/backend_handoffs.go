package monitor

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/internal/models"
)

type backendHandoffReader interface {
	ListIssuesIncludingDeleted(context.Context) ([]backendstore.Issue, error)
	IssueActivityIncludingDeleted(context.Context, string) ([]models.Activity, error)
}

func (s *BackendDataSource) Handoffs() HandoffsDataMsg {
	fail := func(err error) HandoffsDataMsg { return HandoffsDataMsg{Error: err} }
	if s.guard == nil {
		return fail(fmt.Errorf("backend monitor context unavailable"))
	}
	if err := s.guard(s.ctx); err != nil {
		return fail(err)
	}
	reader, ok := s.reader.(backendHandoffReader)
	if !ok {
		return fail(fmt.Errorf("backend handoff reader unavailable"))
	}
	rows, err := reader.ListIssuesIncludingDeleted(s.ctx)
	if err != nil {
		return fail(err)
	}
	issues := map[string]bool{}
	activities := map[string]bool{}
	out := []models.Handoff{}
	for _, row := range rows {
		if err := s.ctx.Err(); err != nil {
			return fail(err)
		}
		if !backendstore.ValidIssueID(row.ID) || issues[row.ID] {
			return fail(fmt.Errorf("invalid or repeated backend issue in handoff listing"))
		}
		issues[row.ID] = true
		events, err := reader.IssueActivityIncludingDeleted(s.ctx, row.ID)
		if err != nil {
			return fail(fmt.Errorf("handoffs on %s: %w", row.ID, err))
		}
		for _, event := range events {
			if event.ID == "" || activities[event.ID] || event.IssueID != row.ID {
				return fail(fmt.Errorf("invalid or repeated backend handoff activity on %s", row.ID))
			}
			activities[event.ID] = true
			if event.Kind == "handoff" {
				out = append(out, models.Handoff{ID: event.ID, IssueID: row.ID, SessionID: event.SessionID, Timestamp: event.CreatedAt, Done: slices.Clone(event.Done), Remaining: slices.Clone(event.Remaining), Decisions: slices.Clone(event.Decisions), Uncertain: slices.Clone(event.Uncertain)})
			}
		}
	}
	if err := s.guard(s.ctx); err != nil {
		return fail(err)
	}
	slices.SortFunc(out, func(a, b models.Handoff) int {
		if cmp := b.Timestamp.Compare(a.Timestamp); cmp != 0 {
			return cmp
		}
		return strings.Compare(a.ID, b.ID)
	})
	if len(out) > 50 {
		out = out[:50]
	}
	return HandoffsDataMsg{Data: out}
}

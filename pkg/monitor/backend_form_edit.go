package monitor

import (
	"context"
	"fmt"

	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/internal/models"
)

type backendEditReader interface {
	Detail(context.Context, string) (backendstore.Detail, error)
}

func (t *backendIssueTransition) PrepareEdit() (models.Issue, []string, error) {
	fail := func(err error) (models.Issue, []string, error) { return models.Issue{}, nil, err }
	if t.source.guard == nil {
		return fail(fmt.Errorf("backend monitor context unavailable"))
	}
	if err := t.source.guard(t.source.ctx); err != nil {
		return fail(err)
	}
	reader, ok := t.source.reader.(backendEditReader)
	if !ok {
		return fail(fmt.Errorf("backend form detail reader unavailable"))
	}
	detail, err := reader.Detail(t.source.ctx, t.observed.ID)
	if err != nil {
		return fail(err)
	}
	if err := t.source.guard(t.source.ctx); err != nil {
		return fail(err)
	}
	if detail.Issue.ID != t.observed.ID || detail.Issue.Revision != t.observed.Revision || detail.Issue.DeletedAt != nil {
		return fail(fmt.Errorf("issue changed since it was displayed; refresh before editing"))
	}
	deps := make([]string, 0, len(detail.Dependencies))
	for _, edge := range detail.Dependencies {
		if edge.IssueID != t.observed.ID || !backendstore.ValidIssueID(edge.DependsOnID) {
			return fail(fmt.Errorf("invalid backend dependency observation"))
		}
		deps = append(deps, edge.DependsOnID)
	}
	return detail.Issue.Issue, deps, nil
}

package monitor

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/internal/models"
)

type backendAutofillReader interface {
	List(context.Context, bool) ([]backendstore.Issue, error)
}

func (s *BackendDataSource) Autofill() AutofillResultMsg {
	fail := func(err error) AutofillResultMsg { return AutofillResultMsg{Error: err} }
	if s.guard == nil {
		return fail(fmt.Errorf("backend monitor context unavailable"))
	}
	if err := s.guard(s.ctx); err != nil {
		return fail(err)
	}
	reader, ok := s.reader.(backendAutofillReader)
	if !ok {
		return fail(fmt.Errorf("backend autocomplete reader unavailable"))
	}
	rows, err := reader.List(s.ctx, true)
	if err != nil {
		return fail(err)
	}
	if err := s.guard(s.ctx); err != nil {
		return fail(err)
	}
	issues := make([]backendstore.Issue, 0, len(rows))
	seen := map[string]bool{}
	for _, row := range rows {
		if !backendstore.ValidIssueID(row.ID) || seen[row.ID] {
			return fail(fmt.Errorf("invalid or repeated backend autocomplete issue"))
		}
		seen[row.ID] = true
		if row.DeletedAt != nil || row.Status == models.StatusClosed {
			continue
		}
		if !models.IsValidType(row.Type) || !models.IsValidPriority(row.Priority) || strings.TrimSpace(row.Title) == "" {
			return fail(fmt.Errorf("invalid backend autocomplete issue"))
		}
		issues = append(issues, row)
	}
	slices.SortFunc(issues, func(a, b backendstore.Issue) int {
		if cmp := strings.Compare(string(a.Priority), string(b.Priority)); cmp != 0 {
			return cmp
		}
		if cmp := b.UpdatedAt.Compare(a.UpdatedAt); cmp != 0 {
			return cmp
		}
		return strings.Compare(a.ID, b.ID)
	})
	if len(issues) > 500 {
		issues = issues[:500]
	}
	items := make([]AutofillItem, len(issues))
	for i, row := range issues {
		items[i] = AutofillItem{ID: row.ID, Title: row.Title, Type: row.Type}
	}
	return AutofillResultMsg{Items: items}
}

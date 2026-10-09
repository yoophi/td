package monitor

import (
	"fmt"

	"github.com/marcus/td/internal/issuestore"
	"github.com/marcus/td/internal/models"
)

type MonitorAutofillSource interface{ Autofill() AutofillResultMsg }

// Autofill reads all tasks before filtering and limiting, so native closed tasks
// cannot consume the candidate limit. Auxiliary board issues are excluded by the store.
func (s *GitHubDataSource) Autofill() AutofillResultMsg {
	fail := func(err error) AutofillResultMsg { return AutofillResultMsg{Error: err} }
	if err := s.ctx.Err(); err != nil {
		return fail(err)
	}
	c, err := s.open(s.ctx)
	if err != nil {
		return fail(err)
	}
	records, err := c.ListIncludingDeleted(s.ctx, true)
	if err != nil {
		return fail(err)
	}
	seen := map[string]bool{}
	issues := []models.Issue{}
	for _, r := range records {
		if err := s.ctx.Err(); err != nil {
			return fail(err)
		}
		if r.ID == "" || seen[r.ID] {
			return fail(fmt.Errorf("invalid or repeated autocomplete task %q", r.ID))
		}
		seen[r.ID] = true
		if r.DeletedAt != nil {
			continue
		}
		switch r.Status {
		case models.StatusOpen, models.StatusInProgress, models.StatusBlocked, models.StatusInReview:
			issues = append(issues, r.Issue)
		}
	}
	if err := issuestore.SortGitHubIssues(issues, "priority", false); err != nil {
		return fail(err)
	}
	if len(issues) > 500 {
		issues = issues[:500]
	}
	items := make([]AutofillItem, len(issues))
	for i, issue := range issues {
		items[i] = AutofillItem{ID: issue.ID, Title: issue.Title, Type: issue.Type}
	}
	if err := s.ctx.Err(); err != nil {
		return fail(err)
	}
	return AutofillResultMsg{Items: items}
}

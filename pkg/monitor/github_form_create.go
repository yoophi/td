package monitor

import (
	"context"
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
)

type MonitorCreateSource interface {
	CreateIssue(models.Issue, []string) (string, error)
}
type githubFormCreator interface {
	Create(context.Context, *models.Issue) (*ghstore.Record, error)
	CreateWithRelations(context.Context, *models.Issue, ghstore.RelationChanges) (*ghstore.Record, error)
}

func (s *GitHubDataSource) CreateIssue(issue models.Issue, deps []string) (string, error) {
	if err := s.ctx.Err(); err != nil {
		return "", err
	}
	if strings.TrimSpace(s.actor) == "" {
		return "", fmt.Errorf("form creation requires actual session")
	}
	if issue.ID != "" || (issue.Status != "" && issue.Status != models.StatusOpen) {
		return "", fmt.Errorf("new form requires an unidentified open issue")
	}
	if strings.TrimSpace(issue.Title) == "" {
		return "", fmt.Errorf("title is required")
	}
	desired := []string{}
	for _, id := range deps {
		n, err := ghstore.Number(id)
		if err != nil {
			return "", err
		}
		canonical := fmt.Sprintf("gh-%d", n)
		if slices.Contains(desired, canonical) {
			return "", fmt.Errorf("duplicate dependency %s", canonical)
		}
		desired = append(desired, canonical)
	}
	raw, err := s.open(s.ctx)
	if err != nil {
		return "", err
	}
	c, ok := raw.(githubFormCreator)
	if !ok {
		return "", fmt.Errorf("GitHub monitor form creator unavailable")
	}
	issue.Status = models.StatusOpen
	issue.CreatorSession = s.actor
	issue.CreatedBranch = s.branch
	issue.ImplementerSession = ""
	issue.ReviewerSession = ""
	issue.ClosedBySession = ""
	var root *ghstore.Record
	if len(desired) == 0 {
		root, err = c.Create(s.ctx, &issue)
	} else {
		// The shared command preflights the graph in one bulk observation and
		// includes all initial dependencies in the actual creation POST.
		root, err = c.CreateWithRelations(s.ctx, &issue, ghstore.RelationChanges{DependsOn: &desired})
	}
	if err != nil {
		if root != nil && root.ID != "" {
			return root.ID, fmt.Errorf("%s was created; inspect the existing task and its command receipt before continuing: %w", root.ID, err)
		}
		return "", err
	}
	if root == nil || root.ID == "" {
		return "", fmt.Errorf("creation result missing; inspect recent GitHub issues before retrying")
	}
	return root.ID, nil
}
func (m Model) submitRemoteCreate() (tea.Model, tea.Cmd) {
	if m.FormCreateAttempted {
		m.StatusMessage = "Creation already attempted; inspect GitHub before reopening the form"
		m.StatusIsError = true
		return m, nil
	}
	source, ok := m.DataSource.(MonitorCreateSource)
	if !ok {
		m.StatusMessage = "GitHub monitor form creator unavailable"
		m.StatusIsError = true
		return m, nil
	}
	issue := *m.FormState.ToIssue()
	issue.Status = models.StatusOpen
	deps := m.FormState.GetDependencies()
	m.FormCreateAttempted = true
	m.WorkflowRequest++
	m.WorkflowPending = true
	m.WorkflowWriting = true
	request, formRequest := m.WorkflowRequest, m.FormAutofillRequest
	return m, func() tea.Msg {
		id, err := source.CreateIssue(issue, deps)
		return MonitorFormSavedMsg{IssueID: id, Request: request, FormRequest: formRequest, Created: true, Error: err}
	}
}

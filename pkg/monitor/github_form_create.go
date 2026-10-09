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
	Get(context.Context, string) (*ghstore.Record, error)
	Create(context.Context, *models.Issue) (*ghstore.Record, error)
	ChangeDependencyObserved(context.Context, *ghstore.Record, string, bool, string) (*ghstore.Record, error)
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
	// Reject missing/deleted targets before creating a task. The subsequent add
	// re-reads and verifies its graph; this is not a transaction with creation.
	for _, id := range desired {
		target, err := c.Get(s.ctx, id)
		if err != nil {
			return "", fmt.Errorf("read dependency %s before creating: %w", id, err)
		}
		if target == nil || target.ID != id || target.DeletedAt != nil {
			return "", fmt.Errorf("dependency %s unavailable", id)
		}
	}
	issue.Status = models.StatusOpen
	issue.CreatorSession = s.actor
	issue.CreatedBranch = s.branch
	issue.ImplementerSession = ""
	issue.ReviewerSession = ""
	issue.ClosedBySession = ""
	root, err := c.Create(s.ctx, &issue)
	if err != nil {
		return "", err
	} // Store errors retain operation-ID recovery guidance.
	if root == nil || root.ID == "" {
		return "", fmt.Errorf("creation result missing; inspect recent GitHub issues before retrying")
	}
	id := root.ID
	for _, target := range desired {
		root, err = c.ChangeDependencyObserved(s.ctx, root, target, true, s.actor)
		if err != nil {
			return id, fmt.Errorf("%s was created, but dependency %s failed; inspect and edit that task rather than creating again: %w", id, target, err)
		}
	}
	return id, nil
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
	request, formRequest := m.WorkflowRequest, m.FormAutofillRequest
	return m, func() tea.Msg {
		id, err := source.CreateIssue(issue, deps)
		return MonitorFormSavedMsg{IssueID: id, Request: request, FormRequest: formRequest, Created: true, Error: err}
	}
}

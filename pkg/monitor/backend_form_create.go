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

type backendCreateWriter interface {
	RunIntent(context.Context, any, func() (backendstore.MutationRequest, error), any, func() error) error
}

func (s *BackendDataSource) CreateIssue(issue models.Issue, deps []string) (string, error) {
	if s.guard == nil {
		return "", fmt.Errorf("backend monitor context unavailable")
	}
	if issue.ID != "" || issue.Status != "" && issue.Status != models.StatusOpen || strings.TrimSpace(issue.Title) == "" {
		return "", fmt.Errorf("new backend issue requires a title, no ID and open status")
	}
	if issue.Type == "" {
		issue.Type = models.TypeTask
	}
	if issue.Priority == "" {
		issue.Priority = models.PriorityP2
	}
	if !models.IsValidType(issue.Type) || !models.IsValidPriority(issue.Priority) || issue.Points != 0 && !models.IsValidPoints(issue.Points) {
		return "", fmt.Errorf("invalid backend issue type, priority or points")
	}
	if issue.ParentID != "" && !backendstore.ValidIssueID(issue.ParentID) {
		return "", fmt.Errorf("invalid backend parent issue ID")
	}
	seen := map[string]bool{}
	for _, id := range deps {
		if !backendstore.ValidIssueID(id) || seen[id] {
			return "", fmt.Errorf("invalid or repeated backend dependency %q", id)
		}
		seen[id] = true
	}
	writer, ok := s.reader.(backendCreateWriter)
	if !ok {
		return "", fmt.Errorf("backend form creator unavailable")
	}
	branch := ""
	actor := ""
	if conn, ok := s.reader.(*backendstore.Connection); ok {
		branch, actor = conn.Branch, conn.SessionID
	}
	body := map[string]any{"title": strings.TrimSpace(issue.Title), "description": issue.Description, "acceptance": issue.Acceptance, "type": issue.Type, "priority": issue.Priority, "points": issue.Points, "labels": append([]string{}, issue.Labels...), "sprint": issue.Sprint, "minor": issue.Minor, "created_branch": branch, "depends_on": slices.Clone(deps)}
	if issue.ParentID != "" {
		body["parent_id"] = issue.ParentID
	}
	if issue.DueDate != nil {
		body["due_date"] = *issue.DueDate
	}
	if issue.DeferUntil != nil {
		body["defer_until"] = *issue.DeferUntil
	}
	input := struct {
		Kind string
		Body map[string]any
	}{"issue.create.form", body}
	var reply struct {
		Issue backendstore.Issue `json:"issue"`
	}
	err := writer.RunIntent(s.ctx, input, func() (backendstore.MutationRequest, error) {
		if err := s.guard(s.ctx); err != nil {
			return backendstore.MutationRequest{}, err
		}
		raw, err := json.Marshal(body)
		return backendstore.MutationRequest{Method: "POST", Resource: "/issues", Body: raw}, err
	}, &reply, func() error {
		if err := backendstore.ValidateIssue(reply.Issue); err != nil {
			return err
		}
		if reply.Issue.Status != models.StatusOpen || actor != "" && reply.Issue.CreatorSession != actor {
			return fmt.Errorf("backend created a different issue state or caller")
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return reply.Issue.ID, nil
}

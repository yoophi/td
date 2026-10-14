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

type backendEditWriter interface {
	backendEditReader
	RunGroupIntent(context.Context, any, func() (json.RawMessage, error), func(string, json.RawMessage) error) error
	RunGroupStepIntent(context.Context, any, func() (backendstore.MutationRequest, error), any, func() error) error
}

type backendFormPlan struct {
	IssueID  string         `json:"issue_id"`
	Revision string         `json:"revision"`
	Before   models.Issue   `json:"before"`
	Desired  models.Issue   `json:"desired"`
	OldDeps  []string       `json:"old_deps"`
	Patch    map[string]any `json:"patch"`
	Remove   []string       `json:"remove"`
	Add      []string       `json:"add"`
	Status   models.Status  `json:"status"`
}

func (t *backendIssueTransition) SaveEdit(issue models.Issue, deps []string) error {
	if t.source.guard == nil {
		return fmt.Errorf("backend monitor context unavailable")
	}
	if issue.ID != t.observed.ID || !backendstore.ValidIssueID(issue.ID) || strings.TrimSpace(issue.Title) == "" {
		return fmt.Errorf("invalid backend form issue or title")
	}
	issue.Title = strings.TrimSpace(issue.Title)
	labels := make([]string, 0, len(issue.Labels))
	for _, label := range issue.Labels {
		label = strings.TrimSpace(label)
		if label == "" {
			return fmt.Errorf("backend form label cannot be empty")
		}
		labels = append(labels, label)
	}
	slices.Sort(labels)
	issue.Labels = slices.Compact(labels)
	if !models.IsValidType(issue.Type) || !models.IsValidPriority(issue.Priority) || issue.Points != 0 && !models.IsValidPoints(issue.Points) || !models.IsValidStatus(issue.Status) {
		return fmt.Errorf("invalid backend form type, priority, points or status")
	}
	if issue.ParentID != "" && !backendstore.ValidIssueID(issue.ParentID) {
		return fmt.Errorf("invalid backend form parent")
	}
	seen := map[string]bool{}
	for _, dep := range deps {
		if !backendstore.ValidIssueID(dep) || dep == issue.ID || seen[dep] {
			return fmt.Errorf("invalid or repeated backend dependency %q", dep)
		}
		seen[dep] = true
	}
	writer, ok := t.source.reader.(backendEditWriter)
	if !ok {
		return fmt.Errorf("backend form writer unavailable")
	}
	input := struct {
		Kind, IssueID, Revision string
		Fields                  models.Issue
		Dependencies            []string
	}{"issue.edit.form", issue.ID, t.observed.Revision, issue, slices.Clone(deps)}
	return writer.RunGroupIntent(t.source.ctx, input, func() (json.RawMessage, error) {
		if err := t.source.guard(t.source.ctx); err != nil {
			return nil, err
		}
		detail, err := writer.Detail(t.source.ctx, issue.ID)
		if err != nil {
			return nil, err
		}
		if err := t.source.guard(t.source.ctx); err != nil {
			return nil, err
		}
		if detail.Issue.Revision != t.observed.Revision || detail.Issue.DeletedAt != nil {
			return nil, fmt.Errorf("issue changed since form opened; refresh before editing")
		}
		plan, err := prepareBackendFormPlan(detail, issue, deps)
		if err != nil {
			return nil, err
		}
		return json.Marshal(plan)
	}, func(key string, raw json.RawMessage) error {
		var plan backendFormPlan
		if err := json.Unmarshal(raw, &plan); err != nil {
			return err
		}
		if plan.IssueID != issue.ID || plan.Revision != t.observed.Revision {
			return fmt.Errorf("backend form plan differs from observation")
		}
		return t.applyBackendFormPlan(writer, key, plan)
	})
}

func prepareBackendFormPlan(detail backendstore.Detail, desired models.Issue, deps []string) (backendFormPlan, error) {
	old := detail.Issue.Issue
	plan := backendFormPlan{IssueID: old.ID, Revision: detail.Issue.Revision, Before: old, Desired: desired, OldDeps: []string{}, Patch: map[string]any{}, Status: desired.Status}
	put := func(name string, value any, changed bool) {
		if changed {
			plan.Patch[name] = value
		}
	}
	put("title", desired.Title, desired.Title != old.Title)
	put("description", desired.Description, desired.Description != old.Description)
	put("acceptance", desired.Acceptance, desired.Acceptance != old.Acceptance)
	put("type", desired.Type, desired.Type != old.Type)
	put("priority", desired.Priority, desired.Priority != old.Priority)
	put("points", desired.Points, desired.Points != old.Points)
	put("labels", desired.Labels, !slices.Equal(desired.Labels, old.Labels))
	put("parent_id", desired.ParentID, desired.ParentID != old.ParentID)
	put("minor", desired.Minor, desired.Minor != old.Minor)
	for _, edge := range detail.Dependencies {
		if edge.IssueID != old.ID || !backendstore.ValidIssueID(edge.DependsOnID) || edge.ID == "" || slices.Contains(plan.OldDeps, edge.DependsOnID) {
			return backendFormPlan{}, fmt.Errorf("invalid backend dependency observation")
		}
		plan.OldDeps = append(plan.OldDeps, edge.DependsOnID)
		if !slices.Contains(deps, edge.DependsOnID) {
			plan.Remove = append(plan.Remove, edge.DependsOnID)
		}
	}
	for _, dep := range deps {
		if !slices.Contains(plan.OldDeps, dep) {
			plan.Add = append(plan.Add, dep)
		}
	}
	return plan, nil
}

func (t *backendIssueTransition) applyBackendFormPlan(writer backendEditWriter, key string, plan backendFormPlan) error {
	steps := []struct{ kind, target string }{}
	if len(plan.Patch) != 0 {
		steps = append(steps, struct{ kind, target string }{"patch", ""})
	}
	for _, dep := range plan.Remove {
		steps = append(steps, struct{ kind, target string }{"remove", dep})
	}
	for _, dep := range plan.Add {
		steps = append(steps, struct{ kind, target string }{"add", dep})
	}
	if plan.Status != plan.Before.Status {
		steps = append(steps, struct{ kind, target string }{"status", ""})
	}
	for index, step := range steps {
		identity := struct {
			Kind, Group, Issue, Target string
			Index                      int
		}{"issue.edit.form.step", key, plan.IssueID, step.target, index}
		var reply struct {
			Issue      backendstore.Issue     `json:"issue"`
			Dependency models.IssueDependency `json:"dependency"`
			Removed    bool                   `json:"removed"`
		}
		err := writer.RunGroupStepIntent(t.source.ctx, identity, func() (backendstore.MutationRequest, error) {
			if err := t.source.guard(t.source.ctx); err != nil {
				return backendstore.MutationRequest{}, err
			}
			detail, err := writer.Detail(t.source.ctx, plan.IssueID)
			if err != nil {
				return backendstore.MutationRequest{}, err
			}
			if err := t.source.guard(t.source.ctx); err != nil {
				return backendstore.MutationRequest{}, err
			}
			if detail.Issue.DeletedAt != nil || !backendFormStateMatches(plan, steps[:index], detail) {
				return backendstore.MutationRequest{}, fmt.Errorf("issue changed during form save; refresh before editing")
			}
			request := backendstore.MutationRequest{Revision: detail.Issue.Revision, Resource: "/issues/" + plan.IssueID}
			switch step.kind {
			case "patch":
				request.Method = "PATCH"
				request.Body, err = json.Marshal(plan.Patch)
			case "remove":
				for _, edge := range detail.Dependencies {
					if edge.DependsOnID == step.target {
						request.Method = "DELETE"
						request.Resource += "/dependencies/" + edge.ID
						return request, nil
					}
				}
				return request, fmt.Errorf("dependency %s disappeared during form save", step.target)
			case "add":
				request.Method = "POST"
				request.Resource += "/dependencies"
				request.Body, err = json.Marshal(map[string]string{"depends_on": step.target})
			case "status":
				request.Method = "POST"
				request.Resource += "/status"
				request.Body, err = json.Marshal(map[string]models.Status{"status": plan.Status})
			}
			return request, err
		}, &reply, func() error {
			switch step.kind {
			case "patch":
				if err := backendstore.ValidateIssue(reply.Issue); err != nil {
					return err
				}
				if reply.Issue.ID != plan.IssueID || !backendFormFieldsMatch(plan, true, reply.Issue.Issue) {
					return fmt.Errorf("backend form patch response differs from request")
				}
			case "remove":
				if !reply.Removed {
					return fmt.Errorf("backend did not confirm dependency removal")
				}
			case "add":
				if reply.Dependency.ID == "" || reply.Dependency.IssueID != plan.IssueID || reply.Dependency.DependsOnID != step.target {
					return fmt.Errorf("backend dependency response differs from request")
				}
			case "status":
				if err := backendstore.ValidateIssue(reply.Issue); err != nil {
					return err
				}
				if reply.Issue.ID != plan.IssueID || reply.Issue.Status != plan.Status {
					return fmt.Errorf("backend status response differs from request")
				}
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("backend form save stopped at %s %s: %w", step.kind, step.target, err)
		}
	}
	return nil
}

func backendFormStateMatches(plan backendFormPlan, completed []struct{ kind, target string }, detail backendstore.Detail) bool {
	if len(completed) == 0 && detail.Issue.Revision != plan.Revision {
		return false
	}
	patched, status := false, plan.Before.Status
	deps := slices.Clone(plan.OldDeps)
	for _, step := range completed {
		switch step.kind {
		case "patch":
			patched = true
		case "remove":
			deps = slices.DeleteFunc(deps, func(id string) bool { return id == step.target })
		case "add":
			deps = append(deps, step.target)
		case "status":
			status = plan.Status
		}
	}
	if detail.Issue.Status != status || !backendFormFieldsMatch(plan, patched, detail.Issue.Issue) || len(detail.Dependencies) != len(deps) {
		return false
	}
	for _, edge := range detail.Dependencies {
		if edge.IssueID != plan.IssueID || !slices.Contains(deps, edge.DependsOnID) {
			return false
		}
	}
	return true
}

func backendFormFieldsMatch(plan backendFormPlan, patched bool, current models.Issue) bool {
	want := plan.Before
	if patched {
		want = plan.Desired
	}
	return current.Title == want.Title && current.Description == want.Description && current.Acceptance == want.Acceptance && current.Type == want.Type && current.Priority == want.Priority && current.Points == want.Points && slices.Equal(current.Labels, want.Labels) && current.ParentID == want.ParentID && current.Minor == want.Minor
}

package ghstore

import (
	"context"
	"crypto/rand"
	"fmt"
	"slices"
	"strings"
	"time"
)

type DependencyNotFoundError struct{ IssueID, DependsOnID string }

func (e *DependencyNotFoundError) Error() string {
	return fmt.Sprintf("dependency %s -> %s not found", e.IssueID, e.DependsOnID)
}

// ChangeDependencyObserved preserves the caller's source revision, including
// HTTP If-Match. Add checks the entire reachable target graph using direct GETs
// and verifies its observed revisions before and after writing the source.
// Remove permits cleaning an edge whose target was deleted or became missing.
// These are optimistic checks, not an atomic graph transaction or GitHub CAS.
func (c *Client) ChangeDependencyObserved(ctx context.Context, observed *Record, target string, add bool, actor string) (*Record, error) {
	result, _, err := c.changeDependenciesObserved(ctx, observed, []string{target}, add, actor, false)
	return result, err
}

// DependencyChange describes one requested edge in its original argument order.
// Repeated additions retain the CLI's already-exists reporting without writes.
type DependencyChange struct {
	TargetID      string
	AlreadyExists bool
}

// ChangeDependenciesObserved plans all edge changes before any write, then
// updates the single source once. Its one journal input/receipt covers the
// complete delta, including uncertain-result recovery and command-wide undo.
// Existing additions are no-ops; removals still allow missing/deleted targets.
func (c *Client) ChangeDependenciesObserved(ctx context.Context, observed *Record, targets []string, add bool, actor string) (*Record, []DependencyChange, error) {
	return c.changeDependenciesObserved(ctx, observed, targets, add, actor, true)
}

func (c *Client) changeDependenciesObserved(ctx context.Context, observed *Record, targets []string, add bool, actor string, allowExisting bool) (*Record, []DependencyChange, error) {
	if observed == nil || observed.repository != c.repo || observed.revision == ([32]byte{}) {
		return nil, nil, fmt.Errorf("dependency change requires an observation from this repository")
	}
	if observed.DeletedAt != nil {
		return nil, nil, workflowStateError("cannot change dependencies of deleted issue %s", observed.ID)
	}
	if strings.TrimSpace(actor) == "" || len(targets) == 0 {
		return nil, nil, &WorkflowInputError{Reason: "dependency change requires a session and targets"}
	}
	details, err := observed.CopyDetails()
	if err != nil {
		return nil, nil, err
	}
	changes := make([]DependencyChange, 0, len(targets))
	changed, existing := []string{}, []string{}
	for _, raw := range targets {
		n, err := Number(raw)
		if err != nil {
			return nil, nil, &WorkflowInputError{Reason: err.Error()}
		}
		target := fmt.Sprintf("gh-%d", n)
		exists := slices.Contains(details.Dependencies, target)
		if add && exists && !allowExisting {
			return nil, nil, workflowStateError("dependency already exists")
		}
		if !add && !exists {
			return nil, nil, &DependencyNotFoundError{IssueID: observed.ID, DependsOnID: target}
		}
		changes = append(changes, DependencyChange{TargetID: target, AlreadyExists: add && exists})
		if add && exists {
			if !slices.Contains(changed, target) && !slices.Contains(existing, target) {
				existing = append(existing, target)
			}
			continue
		}
		changed = append(changed, target)
		if add {
			details.Dependencies = append(details.Dependencies, target)
		} else {
			details.Dependencies = slices.DeleteFunc(details.Dependencies, func(id string) bool { return id == target })
		}
	}
	observations := []Record{}
	if add {
		observations, err = c.dependencyObservations(ctx, observed.ID, changed)
		if err != nil {
			return nil, nil, err
		}
		seen := map[string]bool{observed.ID: true}
		for _, r := range observations {
			seen[r.ID] = true
		}
		for _, target := range existing {
			if seen[target] {
				continue
			}
			// Duplicate additions still require a live task. Do not expand
			// its graph or repair unrelated historical edges for a no-op.
			r, err := c.Get(ctx, target)
			if err != nil {
				return nil, nil, fmt.Errorf("validate existing dependency %s: %w", target, err)
			}
			observations = append(observations, *r)
			seen[target] = true
		}
	}
	verify := func(after bool) error {
		verifyCtx := ctx
		if after {
			verifyCtx = withReadbackCost(ctx)
		}
		for _, before := range observations {
			current, err := c.Get(verifyCtx, before.ID)
			if err != nil {
				return fmt.Errorf("verify dependency %s (source write attempted=%v): %w", before.ID, after, err)
			}
			if current.revision != before.revision {
				return &ConflictError{ID: observed.ID, AfterWrite: after}
			}
		}
		return nil
	}
	if err := verify(false); err != nil {
		return nil, nil, err
	}
	if len(changed) == 0 {
		current, err := c.Get(ctx, observed.ID)
		if err != nil {
			return nil, nil, err
		}
		if current.revision != observed.revision {
			return nil, nil, &ConflictError{ID: observed.ID}
		}
		return observed, changes, nil
	}
	now := time.Now().UTC()
	operation := "td-op-" + rand.Text()
	supersedeReviews(&details, now)
	details.ReviewerSession = ""
	details.ReviewedAt = nil
	details.ReviewBasis = ""
	action := "remove_dep"
	if add {
		action = "add_dep"
	}
	for _, target := range changed {
		details.Transitions = append(details.Transitions, TransitionRecord{OperationID: operation, Action: action, From: observed.Status, To: observed.Status, SessionID: actor, At: now, RelatedIssueID: target})
	}
	result, err := c.UpdateObserved(ctx, observed, Changes{Details: &details, journalRelated: observations})
	if err != nil {
		return nil, nil, fmt.Errorf("%s dependencies for %s (operation %s): %w", action, observed.ID, operation, err)
	}
	if err := verify(true); err != nil {
		return nil, nil, fmt.Errorf("%s dependency change for %s was saved (operation %s), but graph verification failed; inspect current state before retrying: %w", action, observed.ID, operation, err)
	}
	return result, changes, nil
}

// Capture the reachable graph used for validation, without refreshing the source.
func (c *Client) dependencyObservations(ctx context.Context, source string, targets []string) ([]Record, error) {
	observations := []Record{}
	visiting, visited := map[string]bool{}, map[string]bool{}
	var walk func(string) error
	walk = func(id string) error {
		if id == source {
			return &WorkflowInputError{Reason: "cannot add dependency: would create circular dependency"}
		}
		if visiting[id] {
			return &WorkflowInputError{Reason: "target dependency graph already contains a circular dependency"}
		}
		if visited[id] {
			return nil
		}
		visiting[id] = true
		r, err := c.getRelationObservation(ctx, id)
		if err != nil {
			return fmt.Errorf("read dependency %s: %w", id, err)
		}
		observations = append(observations, *r)
		if r.Details != nil {
			for _, next := range r.Details.Dependencies {
				if err := walk(next); err != nil {
					return err
				}
			}
		}
		delete(visiting, id)
		visited[id] = true
		return nil
	}
	for _, target := range targets {
		if err := walk(target); err != nil {
			return nil, err
		}
	}
	return observations, nil
}

// ReplaceDependenciesObserved changes one source's complete dependency set in
// one PATCH. Invalid targets never remove existing edges first. Other sources
// (the reverse "blocks" relation) still require separate observed writes.
func (c *Client) ReplaceDependenciesObserved(ctx context.Context, observed *Record, targets []string, actor string) (*Record, bool, error) {
	if observed == nil || observed.repository != c.repo || observed.revision == ([32]byte{}) {
		return nil, false, fmt.Errorf("dependency replacement requires an observation from this repository")
	}
	if observed.DeletedAt != nil || strings.TrimSpace(actor) == "" {
		return nil, false, &WorkflowInputError{Reason: "dependency replacement requires a live issue and session"}
	}
	targets = append([]string{}, targets...)
	seen := map[string]bool{}
	for i, id := range targets {
		n, err := Number(id)
		if err != nil {
			return nil, false, err
		}
		targets[i] = fmt.Sprintf("gh-%d", n)
		if seen[targets[i]] {
			return nil, false, &WorkflowInputError{Reason: "duplicate dependency"}
		}
		seen[targets[i]] = true
	}
	observations, err := c.dependencyObservations(ctx, observed.ID, targets)
	if err != nil {
		return nil, false, err
	}
	details, err := observed.CopyDetails()
	if err != nil {
		return nil, false, err
	}
	if slices.Equal(details.Dependencies, targets) {
		current, err := c.Get(ctx, observed.ID)
		if err != nil {
			return nil, false, err
		}
		if current.revision != observed.revision {
			return nil, false, &ConflictError{ID: observed.ID}
		}
		return observed, true, nil
	}
	verify := func(after bool) error {
		verifyCtx := ctx
		if after {
			verifyCtx = withReadbackCost(ctx)
		}
		for _, previous := range observations {
			current, err := c.Get(verifyCtx, previous.ID)
			if err != nil {
				return fmt.Errorf("verify dependency %s (source write attempted=%t): %w", previous.ID, after, err)
			}
			if current.revision != previous.revision {
				return &ConflictError{ID: observed.ID, AfterWrite: after}
			}
		}
		return nil
	}
	if err := verify(false); err != nil {
		return nil, false, err
	}
	now := time.Now().UTC()
	operation := "td-op-" + rand.Text()
	for _, old := range details.Dependencies {
		if !seen[old] {
			details.Transitions = append(details.Transitions, TransitionRecord{OperationID: operation, Action: "remove_dep", From: observed.Status, To: observed.Status, SessionID: actor, At: now, RelatedIssueID: old})
		}
	}
	for _, target := range targets {
		if !slices.Contains(details.Dependencies, target) {
			details.Transitions = append(details.Transitions, TransitionRecord{OperationID: operation, Action: "add_dep", From: observed.Status, To: observed.Status, SessionID: actor, At: now, RelatedIssueID: target})
		}
	}
	details.Dependencies = targets
	supersedeReviews(&details, now)
	details.ReviewerSession = ""
	details.ReviewedAt = nil
	details.ReviewBasis = ""
	result, err := c.UpdateObserved(ctx, observed, Changes{Details: &details, journalRelated: observations})
	if err != nil {
		return nil, false, fmt.Errorf("replace dependencies %s (operation %s): %w", observed.ID, operation, err)
	}
	if err := verify(true); err != nil {
		return nil, false, fmt.Errorf("dependencies for %s were saved (operation %s), but graph verification failed; earlier changes remain, inspect current state before retrying: %w", observed.ID, operation, err)
	}
	return result, false, nil
}

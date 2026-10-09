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
	if observed == nil || observed.repository != c.repo || observed.revision == ([32]byte{}) {
		return nil, fmt.Errorf("dependency change requires an observation from this repository")
	}
	if observed.DeletedAt != nil {
		return nil, workflowStateError("cannot change dependencies of deleted issue %s", observed.ID)
	}
	if strings.TrimSpace(actor) == "" {
		return nil, &WorkflowInputError{Reason: "dependency change requires a session"}
	}
	n, err := Number(target)
	if err != nil {
		return nil, &WorkflowInputError{Reason: err.Error()}
	}
	target = fmt.Sprintf("gh-%d", n)
	details, err := observed.CopyDetails()
	if err != nil {
		return nil, err
	}
	exists := slices.Contains(details.Dependencies, target)
	if add && exists {
		return nil, workflowStateError("dependency already exists")
	}
	if !add && !exists {
		return nil, &DependencyNotFoundError{IssueID: observed.ID, DependsOnID: target}
	}
	observations := []Record{}
	if add {
		visiting, visited := map[string]bool{}, map[string]bool{}
		var walk func(string) error
		walk = func(id string) error {
			if id == observed.ID {
				return &WorkflowInputError{Reason: "cannot add dependency: would create circular dependency"}
			}
			if visiting[id] {
				return &WorkflowInputError{Reason: "target dependency graph already contains a circular dependency"}
			}
			if visited[id] {
				return nil
			}
			visiting[id] = true
			r, err := c.Get(ctx, id)
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
		if err := walk(target); err != nil {
			return nil, err
		}
		details.Dependencies = append(details.Dependencies, target)
	} else {
		details.Dependencies = slices.DeleteFunc(details.Dependencies, func(id string) bool { return id == target })
	}
	verify := func(after bool) error {
		for _, before := range observations {
			current, err := c.Get(ctx, before.ID)
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
		return nil, err
	}
	now := time.Now().UTC()
	supersedeReviews(&details, now)
	details.ReviewerSession = ""
	details.ReviewedAt = nil
	details.ReviewBasis = ""
	action := "remove_dep"
	if add {
		action = "add_dep"
	}
	details.Transitions = append(details.Transitions, TransitionRecord{OperationID: "td-op-" + rand.Text(), Action: action, From: observed.Status, To: observed.Status, SessionID: actor, At: now, RelatedIssueID: target})
	result, err := c.UpdateObserved(ctx, observed, Changes{Details: &details})
	if err != nil {
		return nil, err
	}
	if err := verify(true); err != nil {
		return nil, fmt.Errorf("%s dependency %s -> %s was saved, but graph verification failed; inspect current state before retrying: %w", action, observed.ID, target, err)
	}
	return result, nil
}

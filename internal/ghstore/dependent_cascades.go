package ghstore

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/marcus/td/internal/models"
)

// dependentGraph observes the root, its direct dependents and every dependency
// used to decide whether to unblock them. Missing dependencies never count as
// closed. Unrelated issues need not retain their revisions during this cascade.
func (c *Client) dependentGraph(ctx context.Context, root string) (map[string]Record, []string, error) {
	records, err := c.List(ctx, true)
	if err != nil {
		return nil, nil, err
	}
	all := make(map[string]Record, len(records))
	for _, record := range records {
		if _, duplicate := all[record.ID]; duplicate {
			return nil, nil, fmt.Errorf("GitHub listing repeated %s; retry the read", record.ID)
		}
		all[record.ID] = record
	}
	if _, exists := all[root]; !exists {
		return nil, nil, fmt.Errorf("issue %s is missing or deleted", root)
	}
	graph := map[string]Record{root: all[root]}
	var dependents []string
	for _, record := range records {
		if record.Details == nil || !slices.Contains(record.Details.Dependencies, root) {
			continue
		}
		graph[record.ID] = record
		dependents = append(dependents, record.ID)
		for _, id := range record.Details.Dependencies {
			if dependency, exists := all[id]; exists {
				graph[id] = dependency
			}
		}
	}
	sort.Strings(dependents)
	return graph, dependents, nil
}

func (c *Client) verifyDependentGraph(ctx context.Context, root string, expected map[string]Record, members []string) error {
	actual, dependents, err := c.dependentGraph(ctx, root)
	if err != nil {
		return err
	}
	if !slices.Equal(members, dependents) || len(actual) != len(expected) {
		return fmt.Errorf("dependent membership or dependency existence changed for %s: %w", root, &ConflictError{ID: root, AfterWrite: true})
	}
	for id, previous := range expected {
		current, exists := actual[id]
		if !exists || current.revision != previous.revision {
			return &ConflictError{ID: id, AfterWrite: true}
		}
	}
	return nil
}

func (c *Client) transitionWithDependents(ctx context.Context, id, action string, o TransitionOptions) (*Record, bool, error) {
	if err := ValidateReviewOptions(action, o); err != nil {
		return nil, false, err
	}
	n, err := Number(id)
	if err != nil {
		return nil, false, err
	}
	rootID := fmt.Sprintf("gh-%d", n)
	graph, dependents, err := c.dependentGraph(ctx, rootID)
	if err != nil {
		return nil, false, err
	}
	root := graph[rootID]
	if o.expectedRevision != nil && root.revision != *o.expectedRevision {
		return nil, false, &ConflictError{ID: rootID}
	}
	o.expectedRevision = &root.revision
	result, noop, err := c.Transition(ctx, rootID, action, o)
	if err != nil {
		return nil, false, err
	}
	if result.Status != models.StatusClosed {
		return result, noop, nil
	}
	graph[rootID] = *result
	completed := []string{rootID}
	fail := func(err error) (*Record, bool, error) {
		return nil, false, fmt.Errorf("%s saved for %s; dependent cascade stopped (earlier changes remain; inspect current state before retrying): %w", action, strings.Join(completed, ", "), err)
	}
	if err := c.verifyDependentGraph(ctx, rootID, graph, dependents); err != nil {
		return fail(err)
	}
	for _, id := range dependents {
		dependent := graph[id]
		if dependent.Status != models.StatusBlocked {
			continue
		}
		allClosed := true
		for _, dependencyID := range dependent.Details.Dependencies {
			dependency, exists := graph[dependencyID]
			if !exists || dependency.Status != models.StatusClosed {
				allClosed = false
				break
			}
		}
		if !allClosed {
			continue
		}
		updated, _, err := c.TransitionObserved(ctx, &dependent, "unblock", TransitionOptions{SessionID: o.SessionID, Mode: o.Mode, Reason: "Auto-unblocked (dependency " + rootID + " closed)"})
		if err != nil {
			return fail(fmt.Errorf("%s: %w", id, err))
		}
		graph[id] = *updated
		completed = append(completed, id)
		result.AutoUnblocked = append(result.AutoUnblocked, *updated)
		if err := c.verifyDependentGraph(ctx, rootID, graph, dependents); err != nil {
			return fail(err)
		}
	}
	return result, noop && len(result.AutoUnblocked) == 0, nil
}

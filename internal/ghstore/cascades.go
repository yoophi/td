package ghstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/marcus/td/internal/models"
)

// TransitionObservedWithCascades carries an HTTP/policy observation through
// graph reads. A fresh graph must never replace a known stale root revision.
func (c *Client) TransitionObservedWithCascades(ctx context.Context, observed *Record, action string, o TransitionOptions) (*Record, bool, error) {
	if observed == nil || observed.repository != c.repo || observed.revision == ([32]byte{}) {
		return nil, false, fmt.Errorf("transition observation from this repository is required")
	}
	revision := observed.revision
	o.expectedRevision = &revision
	return c.TransitionWithCascades(ctx, observed.ID, action, o)
}

// TransitionWithCascades is the command-level transition entry point. Individual
// writes remain optimistic: GitHub provides no transaction over a hierarchy.
func (c *Client) TransitionWithCascades(ctx context.Context, id, action string, o TransitionOptions) (*Record, bool, error) {
	if action != "review" {
		return c.Transition(ctx, id, action, o)
	}
	if err := ValidateReviewOptions(action, o); err != nil {
		return nil, false, err
	}
	n, err := Number(id)
	if err != nil {
		return nil, false, err
	}
	rootID := fmt.Sprintf("gh-%d", n)
	graph, descendants, err := c.reviewGraph(ctx, rootID)
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
	graph[rootID] = *result
	completed := []string{rootID}
	fail := func(err error) (*Record, bool, error) {
		return nil, false, fmt.Errorf("review saved for %s; descendant cascade stopped (earlier changes remain; inspect current state before retrying): %w", strings.Join(completed, ", "), err)
	}
	if err := c.verifyReviewGraph(ctx, rootID, graph); err != nil {
		return fail(err)
	}
	for _, child := range descendants {
		if child.Status != models.StatusOpen && child.Status != models.StatusInProgress {
			continue
		}
		// Each descendant is a separate review request, not approval or an
		// independent-review attestation. Root-only flags (including minor) do not
		// propagate. SQLite likewise waives individual handoffs for descendants.
		options := TransitionOptions{SessionID: o.SessionID, Mode: o.Mode, Reason: "Cascaded review from " + rootID, skipReviewHandoff: true, expectedRevision: &child.revision}
		updated, _, err := c.Transition(ctx, child.ID, "review", options)
		if err != nil {
			return fail(fmt.Errorf("%s: %w", child.ID, err))
		}
		graph[child.ID] = *updated
		completed = append(completed, child.ID)
		result.CascadedReviews = append(result.CascadedReviews, *updated)
		if err := c.verifyReviewGraph(ctx, rootID, graph); err != nil {
			return fail(err)
		}
	}
	return result, noop, nil
}

func (c *Client) reviewGraph(ctx context.Context, root string) (map[string]Record, []Record, error) {
	records, err := c.List(ctx, true)
	if err != nil {
		return nil, nil, err
	}
	descendants, err := descendantsFromRecords(records, root)
	if err != nil {
		return nil, nil, err
	}
	graph := make(map[string]Record, len(descendants)+1)
	for _, record := range records {
		if record.ID == root {
			graph[root] = record
			break
		}
	}
	for _, record := range descendants {
		graph[record.ID] = record
	}
	return graph, descendants, nil
}

// Re-read membership as well as revisions. This detects reparenting, newly
// added children and edits to previously processed children. Reads themselves
// are paginated, not atomic; a later concurrent edit can still escape detection.
func (c *Client) verifyReviewGraph(ctx context.Context, root string, expected map[string]Record) error {
	actual, _, err := c.reviewGraph(ctx, root)
	if err != nil {
		return err
	}
	if len(actual) != len(expected) {
		return fmt.Errorf("descendant membership changed for %s", root)
	}
	for id, previous := range expected {
		current, ok := actual[id]
		if !ok || current.revision != previous.revision {
			return &ConflictError{ID: id, AfterWrite: true}
		}
	}
	return nil
}

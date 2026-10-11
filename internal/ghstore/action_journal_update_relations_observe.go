package ghstore

import (
	"context"
	"fmt"
	"slices"
)

// Only the frozen graph boundary is immutable. Unrelated repository activity
// may continue. Reverse membership additionally catches newly added dependents
// that did not exist in the original observation.
func verifyRelationGroupGraph(g *ActionGroup, records []Record) error {
	if !g.updateRelationsGroup() {
		return nil
	}
	all := map[string]Record{}
	for _, r := range records {
		all[r.ID] = r
	}
	for _, watch := range g.RelationUpdates.Watches {
		r, exists := all[watch.ID]
		if !exists {
			return &ConflictError{ID: watch.ID}
		}
		state, err := actionIssueState(&r)
		if err != nil {
			return err
		}
		if state.Revision != watch.State.Revision {
			return &ConflictError{ID: watch.ID}
		}
	}
	for source, original := range g.RelationUpdates.Blocked {
		expected := map[string]bool{}
		for _, id := range original {
			expected[id] = true
		}
		for _, member := range g.Members {
			r, exists := all[member.IssueID]
			if !exists {
				return &ConflictError{ID: member.IssueID}
			}
			delete(expected, member.IssueID)
			if r.Details != nil && slices.Contains(r.Details.Dependencies, source) {
				expected[member.IssueID] = true
			}
		}
		for _, r := range records {
			present := r.Details != nil && slices.Contains(r.Details.Dependencies, source)
			if present != expected[r.ID] {
				return fmt.Errorf("relationship command reverse membership changed for %s: %w", source, &ConflictError{ID: r.ID})
			}
			delete(expected, r.ID)
		}
		if len(expected) != 0 {
			return fmt.Errorf("relationship command reverse membership disappeared for %s", source)
		}
	}
	return nil
}

func (c *Client) verifyRelationGroupOriginal(ctx context.Context, g *ActionGroup, expected map[string]ActionIssueState) error {
	snapshot, err := c.ReadSnapshot(WithFreshSnapshot(ctx), false)
	if err != nil {
		return err
	}
	records := snapshot.Issues(false)
	seen := map[string]bool{}
	for _, r := range records {
		if want, found := expected[r.ID]; found {
			state, err := actionIssueState(&r)
			if err != nil {
				return err
			}
			if state.Revision != want.Revision {
				return &ConflictError{ID: r.ID}
			}
			seen[r.ID] = true
		}
	}
	if len(seen) != len(expected) {
		return fmt.Errorf("relationship command original target disappeared")
	}
	return verifyRelationGroupGraph(g, records)
}

// Project inverse contents only to check the entire graph before deleting any
// companion comment. These private records never become receipts or clocks.
// Each actual inverse independently repeats the fresh graph/review fences.
func (c *Client) preflightRelationGroupInverse(ctx context.Context, root *ActionJournalRecord, observed *pairedGroupObservation) error {
	if !root.Group.updateRelationsGroup() {
		return nil
	}
	snapshot, err := c.ReadSnapshot(WithFreshSnapshot(ctx), false)
	if err != nil {
		return err
	}
	all := map[string]Record{}
	for _, r := range snapshot.Issues(false) {
		all[r.ID] = r
	}
	for id, want := range observed.States {
		r, exists := all[id]
		if !exists {
			return &ConflictError{ID: id}
		}
		state, err := actionIssueState(&r)
		if err != nil {
			return err
		}
		if state.Revision != want.Revision {
			return &ConflictError{ID: id}
		}
	}
	for i := len(observed.Receipts) - 1; i >= 0; i-- {
		r := observed.Receipts[i]
		if r.Phase != "applied" || r.Action != "update" {
			continue
		}
		if matched, err := relationUndoScope(r); err != nil {
			return err
		} else if matched {
			current := all[r.IssueID]
			if _, err := relationUndoGraph(r, &current, all, false); err != nil {
				return err
			}
		}
		before, err := actionStateRecord(r.Before, r.IssueID)
		if err != nil {
			return err
		}
		all[r.IssueID] = *before
	}
	return nil
}

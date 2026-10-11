package ghstore

import (
	"context"
	"fmt"
	"reflect"
	"slices"
)

type creationInverseFenceKey struct{}

func checkCreationInverseFence(ctx context.Context) error {
	if fence, _ := ctx.Value(creationInverseFenceKey{}).(func() error); fence != nil {
		return fence()
	}
	return nil
}

// This checks the creation boundary independently of the child group's source
// no-op. The latter correctly ceases to match after logical deletion; we never
// substitute a made-up live source into an observation to bypass that guard.
func (c *Client) observeCreationInverse(ctx context.Context, root *ActionJournalRecord, deleted bool) (*Record, *ActionJournalRecord, error) {
	snapshot, err := c.ReadSnapshot(WithFreshSnapshot(ctx), true)
	if err != nil {
		return nil, nil, err
	}
	history, err := snapshot.ActionHistory("")
	if err != nil {
		return nil, nil, err
	}
	var child *ActionJournalRecord
	found := false
	receipts := []*ActionJournalRecord{}
	for j := range history {
		r := &history[j]
		if r.ID == root.ID {
			if !sameCreationChildReceipt(root, r) {
				return nil, nil, fmt.Errorf("creation inverse parent receipt changed")
			}
			found = true
		}
		if r.OperationID == root.CreationCommand.RelationOperationID || r.CreationOwner == root.OperationID {
			if child != nil {
				return nil, nil, fmt.Errorf("creation inverse found ambiguous children")
			}
			if err := validateCreationChild(root, r); err != nil {
				return nil, nil, err
			}
			child = r
		}
		if r.GroupOperationID == root.CreationCommand.RelationOperationID {
			receipts = append(receipts, r)
		}
	}
	if !found {
		return nil, nil, fmt.Errorf("creation inverse lost its original receipt")
	}
	if err := validateCreationChildProgress(root, child); err != nil {
		return nil, nil, err
	}
	cp := (*CreationInverseCheckpoint)(nil)
	if root.CreationCommand.Inverse != nil {
		cp = root.CreationCommand.Inverse.Checkpoint
	}
	if cp != nil {
		slices.SortFunc(receipts, func(a, b *ActionJournalRecord) int { return a.GroupSequence - b.GroupSequence })
		if !slices.EqualFunc(cp.Receipts, receipts, sameCreationChildReceipt) {
			return nil, nil, fmt.Errorf("creation inverse effect receipts changed after checkpoint")
		}
	}
	expected := creationInverseStates(root)
	var source *Record
	for _, r := range snapshot.Issues(true) {
		if r.ID == root.IssueID {
			source = &r
		}
		if r.meta.OperationID == root.OperationID && r.ID != root.IssueID {
			return nil, nil, fmt.Errorf("creation operation acquired another allocation")
		}
		if state, ok := expected[r.ID]; ok {
			// The child executor owns these changing targets until its inverse
			// checkpoint is frozen; all other original graph watches stay exact.
			if child == nil || cp != nil || !slices.Contains(root.CreationCommand.Blocks, r.ID) {
				actual, err := actionIssueState(&r)
				if err != nil {
					return nil, nil, err
				}
				if actual.Revision != state.Revision {
					return nil, nil, &ConflictError{ID: r.ID}
				}
			}
			delete(expected, r.ID)
		}
		if r.ID != root.IssueID && r.Details != nil {
			if r.Details.ParentID == root.IssueID || slices.Contains(r.Details.Dependencies, root.IssueID) && (cp != nil || child == nil || !slices.Contains(root.CreationCommand.Blocks, r.ID)) {
				return nil, nil, fmt.Errorf("creation acquired an outside relationship: %w", &ConflictError{ID: r.ID})
			}
		}
	}
	if source == nil || len(expected) != 0 {
		return nil, nil, fmt.Errorf("creation inverse source or original graph disappeared")
	}
	state, err := actionIssueState(source)
	if err != nil {
		return nil, nil, err
	}
	if !deleted {
		if state.Revision != root.After.Revision {
			return nil, nil, &ConflictError{ID: root.IssueID}
		}
	} else {
		if cp == nil {
			return nil, nil, fmt.Errorf("creation deletion has no relationship checkpoint")
		}
		want, err := root.CreationCommand.Inverse.DeleteInput.contents()
		if err != nil {
			return nil, nil, err
		}
		if !sameActionContents(want, state) || state.UpdatedAt.Before(root.After.UpdatedAt) || root.Restored != nil && root.Restored.Revision != state.Revision {
			return nil, nil, &ConflictError{ID: root.IssueID}
		}
	}
	for _, entry := range snapshot.entries {
		if entry.Issue.Number == source.Number && len(entry.Comments) != 0 {
			return nil, nil, fmt.Errorf("creation acquired native activity: %w", &ConflictError{ID: source.ID})
		}
	}
	// A newly allocated task starts with no close/reopen events. A round trip
	// back to open must not be mistaken for an unchanged creation.
	events, err := c.readStateEvents(ctx, source.ID)
	if err != nil {
		return nil, nil, err
	}
	if len(events) != 0 {
		return nil, nil, &ConflictError{ID: source.ID}
	}
	for id, original := range root.CreationCommand.Events {
		events, err := c.readStateEvents(ctx, id)
		if err != nil {
			return nil, nil, err
		}
		if !reflect.DeepEqual(events, original) {
			return nil, nil, &ConflictError{ID: id}
		}
	}
	return source, child, nil
}

func (c *Client) writeCreationInverseReceipt(ctx context.Context, old *ActionJournalRecord, next ActionJournal, fence func() error) (*ActionJournalRecord, error) {
	if err := validateCreationInverseRewrite(old.ActionJournal, next); err != nil {
		return nil, err
	}
	publication, err := planActionJournalPublication(next)
	if err != nil {
		return nil, err
	}
	if err := c.verifyCreationReceipt(ctx, old); err != nil {
		return nil, err
	}
	if err := fence(); err != nil {
		return nil, err
	}
	n, _ := CommentNumber(old.ID)
	return c.publishJournal(ctx, fmt.Sprintf("/issues/comments/%d", n), publication, old, fence)
}

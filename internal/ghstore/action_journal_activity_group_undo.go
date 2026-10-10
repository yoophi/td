package ghstore

import (
	"context"
	"fmt"
	"reflect"
)

func checkGroupCommentUndoGate(ctx context.Context, member *ActionJournalRecord, current *Record, deleted bool) error {
	gate, _ := ctx.Value(groupUndoContextKey{}).(*groupUndoGate)
	if gate == nil || gate.CommentFence == nil {
		return nil
	}
	return gate.CommentFence(member, current, deleted)
}

// Fence every original receipt, task state and native activity in one fresh
// collection. Forward and inverse callers supply only actually observed states.
func (c *Client) checkActivityGroupState(ctx context.Context, root *ActionJournalRecord, want map[string]*ActionJournalRecord, expected map[string]ActionIssueState, present map[string]bool) error {
	snapshot, err := c.ReadSnapshot(WithFreshSnapshot(ctx), true)
	if err != nil {
		return err
	}
	history, err := snapshot.ActionHistory("")
	if err != nil {
		return err
	}
	states := map[string]ActionIssueState{}
	carriers := 0
	for _, record := range snapshot.records {
		if record.meta.EntityKind == "journal" {
			carriers++
			if record.ID != root.CarrierID {
				return fmt.Errorf("activity group carrier changed")
			}
		}
		state, err := actionIssueState(&record)
		if err != nil {
			return err
		}
		states[record.ID] = state
	}
	if carriers != 1 {
		return fmt.Errorf("activity group needs one original carrier")
	}
	rootFound := false
	seen := map[string]bool{}
	for i := range history {
		receipt := &history[i]
		if receipt.ID == root.ID {
			if !reflect.DeepEqual(receipt, root) {
				return fmt.Errorf("activity group root changed")
			}
			rootFound = true
		}
		if receipt.GroupOperationID == root.OperationID {
			if seen[receipt.IssueID] || !reflect.DeepEqual(receipt, want[receipt.IssueID]) {
				return fmt.Errorf("activity group member changed")
			}
			seen[receipt.IssueID] = true
		}
	}
	if !rootFound || len(seen) != len(want) {
		return fmt.Errorf("activity group receipt membership changed")
	}
	for _, m := range root.Group.Members {
		state, found := states[m.IssueID]
		target := expected[m.IssueID]
		if !found || state.Revision != target.Revision {
			return &ConflictError{ID: m.IssueID}
		}
		r := want[m.IssueID]
		if r == nil {
			probe := *root
			probe.IssueID, probe.CommentInput = m.IssueID, m.CommentInput
			activity, err := c.commentCreationInSnapshot(snapshot, &probe, state)
			if err != nil {
				return err
			}
			if activity != nil {
				return fmt.Errorf("unattempted activity group member acquired a comment")
			}
			continue
		}
		if !present[m.IssueID] && r.CommentAfter != nil {
			if err := deletedCommentInSnapshot(snapshot, r); err != nil {
				return err
			}
			continue
		}
		activity, err := c.commentCreationInSnapshot(snapshot, r, state)
		if err != nil {
			return err
		}
		if (activity != nil) != present[m.IssueID] {
			return fmt.Errorf("activity group comment presence changed for %s", m.IssueID)
		}
	}
	return nil
}

func (c *Client) checkActivityGroupInverse(ctx context.Context, root *ActionJournalRecord, receipts map[string]*ActionJournalRecord, expected map[string]ActionIssueState, member *ActionJournalRecord, current *Record, deleted bool) error {
	if err := validateGroupMember(root, member); err != nil {
		return err
	}
	want, states, present := activityGroupFenceInputs(receipts, expected)
	want[member.IssueID] = member
	state, err := actionIssueState(current)
	if err != nil {
		return err
	}
	states[member.IssueID] = state
	present[member.IssueID] = !deleted
	return c.checkActivityGroupState(ctx, root, want, states, present)
}

func activityGroupFenceInputs(receipts map[string]*ActionJournalRecord, expected map[string]ActionIssueState) (map[string]*ActionJournalRecord, map[string]ActionIssueState, map[string]bool) {
	want := map[string]*ActionJournalRecord{}
	states := map[string]ActionIssueState{}
	present := map[string]bool{}
	for id, receipt := range receipts {
		want[id] = receipt
		present[id] = receipt.CommentAfter != nil && receipt.Phase != "undone"
	}
	for id, state := range expected {
		states[id] = state
	}
	return want, states, present
}

func (c *Client) verifyActivityGroupForward(ctx context.Context, after *Record) error {
	active := c.journal.Group
	receipts := map[string]*ActionJournalRecord{}
	for _, receipt := range active.Receipts {
		receipts[receipt.IssueID] = receipt
	}
	want, states, present := activityGroupFenceInputs(receipts, active.Expected)
	if after != nil {
		state, err := actionIssueState(after)
		if err != nil {
			return err
		}
		states[after.ID], present[after.ID] = state, true
	}
	return c.checkActivityGroupState(ctx, active.Journal, want, states, present)
}

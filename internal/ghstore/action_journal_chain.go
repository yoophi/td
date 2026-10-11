package ghstore

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
)

// An undo receipt links an exact earlier revision to a newly verified server
// revision. Content equality alone never authorizes overwriting a later edit.
func planUndoThroughReceipts(action *ActionJournalRecord, current *Record, sessionID, author string, history []ActionJournalRecord) (*IssueUndoPlan, []ActionJournalRecord, error) {
	fail := func() (*IssueUndoPlan, []ActionJournalRecord, error) {
		return nil, nil, &ConflictError{ID: action.IssueID}
	}
	if err := action.validate(); err != nil {
		return nil, nil, err
	}
	if action.Phase != "applied" || action.SessionID != sessionID || !strings.EqualFold(action.Author, author) {
		return fail()
	}
	state, err := actionIssueState(current)
	if err != nil {
		return nil, nil, err
	}
	if current.ID != action.IssueID {
		return fail()
	}
	// Require the selected receipt itself to agree with the complete fresh read.
	found := false
	for _, observed := range history {
		if observed.ID == action.ID {
			if found || !reflect.DeepEqual(observed, *action) {
				return fail()
			}
			found = true
		}
	}
	if !found {
		return fail()
	}
	expected := action.After.Revision
	seen := map[string]bool{}
	witnesses := []ActionJournalRecord{}
	for expected != state.Revision {
		if seen[expected] {
			return fail()
		}
		seen[expected] = true
		var edge *ActionJournalRecord
		var restored *ActionIssueState
		var edgeWitnesses []ActionJournalRecord
		for i := range history {
			a := &history[i]
			if a.OperationID == action.OperationID || a.GroupOperationID != "" {
				continue
			}
			matches := a.IssueID == action.IssueID && a.Before != nil && a.Before.Revision == expected
			if a.Group != nil {
				for _, member := range a.Group.Members {
					matches = matches || member.IssueID == action.IssueID && member.Before.Revision == expected
				}
			}
			if !matches {
				continue
			}
			// Ambiguous edges and unfinished outcomes are not proof of restoration.
			if edge != nil || a.Phase != "undone" || a.SessionID != sessionID || !strings.EqualFold(a.Author, author) || a.CarrierID != action.CarrierID {
				return fail()
			}
			if a.Group != nil {
				var err error
				restored, edgeWitnesses, err = completedGroupBridge(a, action.IssueID, history)
				if err != nil {
					return nil, nil, err
				}
				edge = a
				continue
			}
			if a.UpdateObservation != nil || a.CommentObservation != nil || a.NativeUndo != nil || a.UndoObservation != nil || a.validate() != nil || (a.Action != "comment_create" && scalarUndoScope(a) != nil) || !sameActionContents(*a.Before, *a.Restored) || a.Restored.UpdatedAt.Before(a.After.UpdatedAt) {
				return fail()
			}
			edge, restored = a, a.Restored
			edgeWitnesses = []ActionJournalRecord{*a}
		}
		if edge == nil {
			return fail()
		}
		witnesses = append(witnesses, edgeWitnesses...)
		expected = restored.Revision
	}
	// Only the ephemeral planner uses the proven revision. Original snapshots
	// remain immutable in the intent and completion receipt.
	proven := *action
	after := *action.After
	after.UpdatedAt = state.UpdatedAt
	after.Revision, err = after.checksum()
	if err != nil {
		return nil, nil, err
	}
	proven.After = &after
	plan, err := proven.planActionUndo(current, sessionID, author)
	return plan, witnesses, err
}

func sameActionContents(a, b ActionIssueState) bool {
	b.UpdatedAt = a.UpdatedAt
	b.Revision, _ = b.checksum()
	return b.Revision == a.Revision
}

func verifyUndoWitnesses(witnesses, history []ActionJournalRecord) error {
	groups := map[string]bool{}
	for _, witness := range witnesses {
		if witness.Group != nil {
			groups[witness.OperationID] = true
			id := witness.IssueID
			for _, member := range witness.Group.Inverse.Members {
				if member.Status == "undone" {
					id = member.IssueID
					break
				}
			}
			if _, _, err := completedGroupBridge(&witness, id, history); err != nil {
				return err
			}
		}
	}
	for _, witness := range witnesses {
		if witness.UpdateObservation != nil || witness.CommentObservation != nil || witness.NativeUndo != nil || witness.UndoObservation != nil || witness.GroupOperationID != "" && !groups[witness.GroupOperationID] {
			return &ConflictError{ID: witness.IssueID}
		}
		matches := 0
		for _, observed := range history {
			if observed.ID == witness.ID {
				if !reflect.DeepEqual(observed, witness) {
					return &ConflictError{ID: witness.IssueID}
				}
				matches++
			}
		}
		if matches != 1 {
			return &ConflictError{ID: witness.IssueID}
		}
		if witness.Group != nil {
			for _, member := range witness.Group.Members {
				if revisionEdgeCount(history, member.IssueID, member.Before.Revision) != 1 {
					return &ConflictError{ID: member.IssueID}
				}
			}
		} else if witness.GroupOperationID == "" && revisionEdgeCount(history, witness.IssueID, witness.Before.Revision) != 1 {
			return &ConflictError{ID: witness.IssueID}
		}
	}
	return nil
}

// planActionUndo never turns a comment inverse into an issue PATCH.
func (a ActionJournalRecord) planActionUndo(current *Record, sessionID, author string) (*IssueUndoPlan, error) {
	if a.Action != "comment_create" {
		return a.PlanIssueUndo(current, sessionID, author)
	}
	if err := a.validate(); err != nil {
		return nil, err
	}
	if sessionID == "" || author == "" || a.SessionID != sessionID || !strings.EqualFold(a.Author, author) {
		return nil, fmt.Errorf("action is not attributed to the current session and GitHub author")
	}
	if current == nil || current.ID != a.IssueID {
		return nil, fmt.Errorf("undo target identity differs from action history")
	}
	if a.Phase == "prepared" || a.Phase == "undo_prepared" {
		return nil, fmt.Errorf("action %s is %s with an unverified outcome; inspect GitHub before any retry", a.OperationID, a.Phase)
	}
	state, err := actionIssueState(current)
	if err != nil {
		return nil, err
	}
	expected := a.After
	if a.Phase == "undone" {
		expected = a.Restored
	}
	if state.Revision != expected.Revision {
		return nil, &ConflictError{ID: a.IssueID}
	}
	if !strings.EqualFold(a.CommentAfter.Author, author) {
		return nil, fmt.Errorf("native comment is not attributed to the current GitHub author")
	}
	return &IssueUndoPlan{AlreadyUndone: a.Phase == "undone"}, nil
}

func (c *Client) planUndo(ctx context.Context, action *ActionJournalRecord, current *Record, sessionID, author string) (*IssueUndoPlan, []ActionJournalRecord, error) {
	if gate, _ := ctx.Value(groupUndoContextKey{}).(*groupUndoGate); gate != nil && gate.Plan != nil {
		plan, err := gate.Plan(action, current, sessionID, author)
		return plan, nil, err
	}
	plan, err := action.planActionUndo(current, sessionID, author)
	var conflict *ConflictError
	if !errors.As(err, &conflict) || action.Phase != "applied" {
		return plan, nil, err
	}
	snapshot, err := c.ReadSnapshot(WithFreshSnapshot(ctx), true)
	if err != nil {
		return nil, nil, err
	}
	history, err := snapshot.ActionHistory("")
	if err != nil {
		return nil, nil, err
	}
	plan, witnesses, err := planUndoThroughReceipts(action, current, sessionID, author, history)
	if err != nil {
		return nil, nil, err
	}
	if err := verifyCommentUndoWitnesses(snapshot, witnesses); err != nil {
		return nil, nil, err
	}
	for i := range witnesses {
		if witnesses[i].Group != nil {
			if witnesses[i].Group.pairedGroup() {
				if err := c.verifyPairedGroupEvents(ctx, &witnesses[i]); err != nil {
					return nil, nil, err
				}
			} else if witnesses[i].Group.updateActivityGroup() {
				if err := c.verifyUpdateActivityEvents(ctx, &witnesses[i]); err != nil {
					return nil, nil, err
				}
			}
			continue // Member receipts carry review/event proofs; the root has no snapshots.
		}
		if err := c.checkUndoOriginalReview(ctx, &witnesses[i], false); err != nil {
			return nil, nil, err
		}
	}
	return plan, witnesses, nil
}

func verifyCommentUndoWitnesses(snapshot *Snapshot, witnesses []ActionJournalRecord) error {
	for i := range witnesses {
		if witnesses[i].Action == "comment_create" {
			if err := deletedCommentInSnapshot(snapshot, &witnesses[i]); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *Client) recheckUndoWitnesses(ctx context.Context, witnesses []ActionJournalRecord) error {
	if len(witnesses) == 0 {
		return nil
	}
	snapshot, err := c.ReadSnapshot(WithFreshSnapshot(ctx), true)
	if err != nil {
		return err
	}
	return c.checkUndoWitnessesSnapshot(ctx, snapshot, witnesses)
}

func (c *Client) checkUndoWitnessesSnapshot(ctx context.Context, snapshot *Snapshot, witnesses []ActionJournalRecord) error {
	history, err := snapshot.ActionHistory("")
	if err != nil {
		return err
	}
	if err := verifyUndoWitnesses(witnesses, history); err != nil {
		return err
	}
	for i := range witnesses {
		if witnesses[i].Group != nil {
			if witnesses[i].Group.pairedGroup() {
				if err := c.verifyPairedGroupEvents(ctx, &witnesses[i]); err != nil {
					return err
				}
			} else if witnesses[i].Group.updateActivityGroup() {
				if err := c.verifyUpdateActivityEvents(ctx, &witnesses[i]); err != nil {
					return err
				}
			}
			continue // Member receipts carry review/event proofs; the root has no snapshots.
		}
		if err := c.checkUndoOriginalReview(ctx, &witnesses[i], false); err != nil {
			return err
		}
	}
	return verifyCommentUndoWitnesses(snapshot, witnesses)
}

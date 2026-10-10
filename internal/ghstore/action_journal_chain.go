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
		for i := range history {
			a := &history[i]
			if a.IssueID != action.IssueID || a.Before == nil || a.Before.Revision != expected || a.OperationID == action.OperationID {
				continue
			}
			// Ambiguous edges and unfinished outcomes are not proof of restoration.
			if edge != nil || a.Phase != "undone" || a.SessionID != sessionID || !strings.EqualFold(a.Author, author) || a.CarrierID != action.CarrierID {
				return fail()
			}
			if a.GroupOperationID != "" || a.UpdateObservation != nil || a.NativeUndo != nil || a.UndoObservation != nil || a.validate() != nil || (a.Action != "comment_create" && scalarUndoScope(a) != nil) || !sameActionContents(*a.Before, *a.Restored) || a.Restored.UpdatedAt.Before(a.After.UpdatedAt) {
				return fail()
			}
			edge = a
		}
		if edge == nil {
			return fail()
		}
		witnesses = append(witnesses, *edge)
		expected = edge.Restored.Revision
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
	for _, witness := range witnesses {
		if witness.GroupOperationID != "" || witness.UpdateObservation != nil || witness.NativeUndo != nil || witness.UndoObservation != nil {
			return &ConflictError{ID: witness.IssueID}
		}
		matches := 0
		edges := 0
		for _, observed := range history {
			if observed.IssueID == witness.IssueID && observed.Before != nil && observed.Before.Revision == witness.Before.Revision {
				edges++
			}
			if observed.ID == witness.ID {
				if !reflect.DeepEqual(observed, witness) {
					return &ConflictError{ID: witness.IssueID}
				}
				matches++
			}
		}
		if matches != 1 || edges != 1 {
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
	history, err := snapshot.ActionHistory("")
	if err != nil {
		return err
	}
	if err := verifyUndoWitnesses(witnesses, history); err != nil {
		return err
	}
	for i := range witnesses {
		if err := c.checkUndoOriginalReview(ctx, &witnesses[i], false); err != nil {
			return err
		}
	}
	return verifyCommentUndoWitnesses(snapshot, witnesses)
}

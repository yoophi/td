package ghstore

import (
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
			if a.validate() != nil || scalarUndoScope(a) != nil || !sameActionContents(*a.Before, *a.Restored) || a.Restored.UpdatedAt.Before(a.After.UpdatedAt) {
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
	plan, err := proven.PlanIssueUndo(current, sessionID, author)
	return plan, witnesses, err
}

func sameActionContents(a, b ActionIssueState) bool {
	b.UpdatedAt = a.UpdatedAt
	b.Revision, _ = b.checksum()
	return b.Revision == a.Revision
}

func verifyUndoWitnesses(witnesses, history []ActionJournalRecord) error {
	for _, witness := range witnesses {
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

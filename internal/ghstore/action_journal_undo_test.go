package ghstore

import (
	"context"
	"errors"
	"github.com/marcus/td/internal/models"
	"testing"
)

func prepareUndoFixture(t *testing.T) *journalWriteFixture {
	t.Helper()
	f := newJournalWriteFixture(t, "")
	if err := f.client.EnableActionJournal(context.Background(), "ses_actor"); err != nil {
		t.Fatal(err)
	}
	body := "After"
	if _, err := f.client.Update(context.Background(), "gh-1", Changes{Description: &body}); err != nil {
		t.Fatal(err)
	}
	f.commentReads = 0
	return f
}

func TestActionUndoScopeKeepsWorkflowAndRelationsPending(t *testing.T) {
	for _, tc := range []struct {
		name          string
		before, after IssueDetails
		supported     bool
	}{
		{name: "scalar", before: IssueDetails{Status: models.StatusOpen}, after: IssueDetails{Status: models.StatusOpen, Minor: true, Sprint: "Next"}, supported: true},
		{name: "status", before: IssueDetails{Status: models.StatusOpen}, after: IssueDetails{Status: models.StatusInProgress}},
		{name: "parent", before: IssueDetails{Status: models.StatusOpen}, after: IssueDetails{Status: models.StatusOpen, ParentID: "gh-2"}},
		{name: "dependency", before: IssueDetails{Status: models.StatusOpen}, after: IssueDetails{Status: models.StatusOpen, Dependencies: []string{"gh-2"}}},
		{name: "claim", before: IssueDetails{Status: models.StatusOpen}, after: IssueDetails{Status: models.StatusOpen, ImplementerSession: "ses_other"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := journalFixture(t)
			body, err := encodeBody("Before", metadata{Type: models.TypeTask, Priority: models.PriorityP2, Details: &tc.before})
			if err != nil {
				t.Fatal(err)
			}
			a.Before.Body = body
			body, err = encodeBody("After", metadata{Type: models.TypeTask, Priority: models.PriorityP1, Details: &tc.after})
			if err != nil {
				t.Fatal(err)
			}
			a.After.Body = body
			err = scalarUndoScope(&ActionJournalRecord{ActionJournal: a})
			if tc.supported {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				var scope *UndoScopeError
				if !errors.As(err, &scope) {
					t.Fatal("unsupported effects were not rejected", err)
				}
			}
		})
	}
}

func TestActionUndoExecutesVerifiedInverseAndIsIdempotent(t *testing.T) {
	f := prepareUndoFixture(t)
	result, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor")
	if err != nil || result.AlreadyUndone || result.Issue.Description != "Before" || result.Journal.Phase != "undone" || f.targetWrites != 2 {
		t.Fatal(result, err, f)
	}
	if result.Journal.Restored == nil || result.Journal.UndoOperationID == "" {
		t.Fatal("missing verified receipt", result)
	}
	writes := f.targetWrites
	receipts := f.receiptWrites
	result, err = f.client.UndoAction(context.Background(), "ghc-101", "ses_actor")
	if err != nil || !result.AlreadyUndone || f.targetWrites != writes || f.receiptWrites != receipts {
		t.Fatal("receipt replay rewrote state", result, err, f)
	}
}

func TestActionUndoRejectsActorConflictAndUncertainOutcomes(t *testing.T) {
	for _, mode := range []string{"undo-actor", "target-conflict", "target-denied", "target-uncertain", "receipt-denied", "receipt-uncertain", "journal-concurrent", "undo-later"} {
		t.Run(mode, func(t *testing.T) {
			f := prepareUndoFixture(t)
			f.mode = mode
			before := f.targetWrites
			result, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor")
			if err == nil || result != nil || f.targetWrites > before+1 {
				t.Fatal("failed undo succeeded or retried", result, err, f)
			}
			if mode == "undo-actor" && f.targetWrites != before {
				t.Fatal("wrong actor wrote target")
			}
			if mode == "target-conflict" && f.issues[1].Body != "Concurrent edit" {
				t.Fatal("concurrent edit overwritten")
			}
			if mode == "undo-later" {
				var conflict *ConflictError
				if !errors.As(err, &conflict) || !conflict.AfterWrite {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestActionUndoPreparedOutcomeCannotBeRetriedAsSuccess(t *testing.T) {
	f := prepareUndoFixture(t)
	f.mode = "target-uncertain"
	if _, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor"); err == nil {
		t.Fatal("uncertain PATCH succeeded")
	}
	f.mode = ""
	writes := f.targetWrites
	if _, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor"); err == nil || f.targetWrites != writes {
		t.Fatal("uncertain undo was replayed", err)
	}
	a, err := decodeActionJournal(*f.comment, "gh-9")
	if err != nil || a.Phase != "undo_prepared" {
		t.Fatal(a, err)
	}
}

func TestActionUndoReceiptFailureAfterTargetRestoration(t *testing.T) {
	for _, mode := range []string{"undo-receipt-denied", "undo-receipt-uncertain"} {
		t.Run(mode, func(t *testing.T) {
			f := prepareUndoFixture(t)
			f.mode = mode
			if result, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor"); err == nil || result != nil || f.issues[1].Body != "Before" {
				t.Fatal("unverified receipt was success", result, err)
			}
			f.mode = ""
			writes := f.targetWrites
			result, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor")
			if f.targetWrites != writes {
				t.Fatal("receipt retry rewrote target")
			}
			if mode == "undo-receipt-denied" {
				if err == nil {
					t.Fatal("unverified restored target was falsely reconciled")
				}
			} else if err != nil || !result.AlreadyUndone {
				t.Fatal("verified existing receipt was not reconciled", result, err)
			}
		})
	}
}

func TestActionUndoSessionAndLaterChangesCannotBeOverwritten(t *testing.T) {
	f := prepareUndoFixture(t)
	if _, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_other"); err == nil || f.targetWrites != 1 {
		t.Fatal("wrong session undid action", err)
	}
	if _, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor"); err != nil {
		t.Fatal(err)
	}
	target := f.issues[1]
	target.Title = "Later title"
	f.issues[1] = target
	if _, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor"); err == nil || f.targetWrites != 2 {
		t.Fatal("old receipt overwrote later edit", err)
	}
}

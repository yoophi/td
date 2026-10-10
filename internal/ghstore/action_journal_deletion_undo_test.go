package ghstore

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func deletionUndoFixture(t *testing.T, restore, reviewed, closed bool) (*journalWriteFixture, func(string), string) {
	t.Helper()
	f, mode := fileUndoFixture(t, reviewed, closed)
	if _, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor"); err != nil {
		t.Fatal(err)
	}
	current, err := f.client.GetIncludingDeleted(context.Background(), "gh-1")
	if err != nil {
		t.Fatal(err)
	}
	if restore {
		current, _, err = f.client.SetDeletedObserved(context.Background(), current, true, "ses_actor", "Fixture delete")
		if err != nil {
			t.Fatal(err)
		}
	}
	original := current.nativeBody
	if _, noop, err := f.client.SetDeletedObserved(context.Background(), current, !restore, "ses_actor", "Fixture visibility change"); err != nil || noop {
		t.Fatal(noop, err)
	}
	mode("")
	return f, mode, original
}

func TestDeletionUndoRestoresVisibilityAndOriginalReview(t *testing.T) {
	for _, restore := range []bool{false, true} {
		for _, tc := range []struct {
			name             string
			reviewed, closed bool
		}{{"open", false, false}, {"reviewed", true, false}, {"closed-approved", true, true}} {
			t.Run(tc.name+map[bool]string{false: "-delete", true: "-restore"}[restore], func(t *testing.T) {
				f, _, original := deletionUndoFixture(t, restore, tc.reviewed, tc.closed)
				a, err := decodeActionJournal(*f.comment, "gh-9")
				if err != nil {
					t.Fatal(err)
				}
				before, after := *a.Before, *a.After
				out, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor")
				if err != nil || out.Issue.nativeBody != original || out.Journal.Phase != "undone" || out.Issue.nativeState != before.State {
					t.Fatal(out, err)
				}
				if !reflect.DeepEqual(before, *out.Journal.Before) || !reflect.DeepEqual(after, *out.Journal.After) {
					t.Fatal("snapshots rewritten")
				}
				writes, receipts := f.targetWrites, f.receiptWrites
				out, err = f.client.UndoAction(context.Background(), "ghc-101", "ses_actor")
				if err != nil || !out.AlreadyUndone || f.targetWrites != writes || f.receiptWrites != receipts {
					t.Fatal(out, err)
				}
			})
		}
	}
}

func TestDeletionUndoRejectsChangedReviewProof(t *testing.T) {
	for _, mode := range []string{"events", "handoff", "events-after-intent", "handoff-after-patch", "events-after-receipt", "target-after-final-review"} {
		t.Run(mode, func(t *testing.T) {
			f, setMode, _ := deletionUndoFixture(t, false, true, false)
			setMode(mode)
			out, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor")
			var conflict *ConflictError
			if out != nil || !errors.As(err, &conflict) {
				t.Fatal(out, err)
			}
		})
	}
}

func TestDeletionUndoRejectsUnrelatedWriterEffects(t *testing.T) {
	for _, mode := range []string{"actor", "timestamp", "claim", "review", "history", "title", "native-state"} {
		t.Run(mode, func(t *testing.T) {
			f, _, _ := deletionUndoFixture(t, false, false, false)
			a, err := decodeActionJournal(*f.comment, "gh-9")
			if err != nil {
				t.Fatal(err)
			}
			desc, m, _, err := decodeBody(a.After.Body)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "actor":
				m.Details.Transitions[len(m.Details.Transitions)-1].SessionID = "ses_foreign"
			case "timestamp":
				m.Details.DeletedAt = &a.Before.UpdatedAt
			case "claim":
				m.Details.ImplementerSession = "ses_foreign"
			case "review":
				m.Details.ReviewBasis = "fabricated"
			case "history":
				m.Details.Transitions = nil
			case "title":
				a.After.Title = "Foreign title"
			case "native-state":
				a.After.State = "closed"
			}
			a.After.Body, err = encodeBody(desc, m)
			if err != nil {
				t.Fatal(err)
			}
			if err = scalarUndoScope(a); err == nil {
				t.Fatal("forged deletion accepted", mode)
			}
		})
	}
}

func TestDeletionUndoUncertainCompletionCanBeObservedWithoutReplay(t *testing.T) {
	for _, restore := range []bool{false, true} {
		t.Run(map[bool]string{false: "delete", true: "restore"}[restore], func(t *testing.T) {
			f, _, original := deletionUndoFixture(t, restore, true, false)
			f.receiptWrites = 1 // Count this inverse after setup journals.
			f.mode = "undo-receipt-denied"
			if _, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor"); err == nil {
				t.Fatal("receipt failure succeeded")
			}
			f.mode = ""
			writes := f.targetWrites
			result, err := f.client.ReconcileAction(context.Background(), "ghc-101", "ses_recovery", true)
			if err != nil || !result.Applied || result.Issue.nativeBody != original || f.targetWrites != writes || result.UndoOutcome.TargetWriteVerified {
				t.Fatal(result, err)
			}
		})
	}
}

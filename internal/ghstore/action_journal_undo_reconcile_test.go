package ghstore

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func pendingInverseFixture(t *testing.T) *journalWriteFixture {
	t.Helper()
	f := prepareUndoFixture(t)
	f.mode = "undo-receipt-denied"
	if _, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor"); err == nil {
		t.Fatal("receipt failure succeeded")
	}
	f.mode = ""
	a, err := decodeActionJournal(*f.comment, "gh-9")
	if err != nil || a.Phase != "undo_prepared" {
		t.Fatal(a, err)
	}
	return f
}

func TestUndoReconcileObservesInverseWithoutReplaying(t *testing.T) {
	f := pendingInverseFixture(t)
	original, err := decodeActionJournal(*f.comment, "gh-9")
	if err != nil {
		t.Fatal(err)
	}
	writes, receipts := f.targetWrites, f.receiptWrites
	result, err := f.client.ReconcileAction(context.Background(), "ghc-101", "ses_recovery", false)
	if err != nil || result.Applied || result.TargetWriteReplayed || f.targetWrites != writes || f.receiptWrites != receipts {
		t.Fatal(result, err)
	}
	result, err = f.client.ReconcileAction(context.Background(), "ghc-101", "ses_recovery", true)
	if err != nil || !result.Applied || result.Journal.Phase != "undone" || f.targetWrites != writes || f.receiptWrites != receipts+1 {
		t.Fatal(result, err)
	}
	if !reflect.DeepEqual(original.Before, result.Journal.Before) || !reflect.DeepEqual(original.After, result.Journal.After) || result.Journal.SessionID != "ses_actor" {
		t.Fatal("original attempt rewritten")
	}
	result, err = f.client.ReconcileAction(context.Background(), "ghc-101", "ses_other_recovery", true)
	if err != nil || !result.AlreadyReconciled || result.Applied || f.targetWrites != writes || f.receiptWrites != receipts+1 {
		t.Fatal("repeat wrote", result, err)
	}
}

func TestUndoReconcileVerifiedCompletionIsReadOnly(t *testing.T) {
	f := prepareUndoFixture(t)
	if _, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor"); err != nil {
		t.Fatal(err)
	}
	writes, receipts := f.targetWrites, f.receiptWrites
	before := *f.comment
	result, err := f.client.ReconcileAction(context.Background(), "ghc-101", "ses_recovery", true)
	if err != nil || !result.AlreadyReconciled || result.Applied || f.targetWrites != writes || f.receiptWrites != receipts || *f.comment != before {
		t.Fatal("completion confirmation wrote", result, err)
	}
}

func TestUndoReconcileObservationCannotAuthorizeCausalChain(t *testing.T) {
	a, current, history := undoChainFixture(t)
	edge := &history[1]
	edge.UndoObservation = &ActionUndoObservation{SessionID: "ses_recovery", Actor: edge.Author, ObservedAt: time.Now().UTC(), Proof: inverseObservationProof, State: edge.Restored}
	if err := edge.validate(); err != nil {
		t.Fatal(err)
	}
	_, _, err := planUndoThroughReceipts(a, current, "ses_actor", "github-author", history)
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatal("observation authorized old revision overwrite", err)
	}
	if err := verifyUndoWitnesses([]ActionJournalRecord{*edge}, history); !errors.As(err, &conflict) {
		t.Fatal("observation accepted as witness", err)
	}
}

func TestUndoReconcileRejectsChangedTargetsAndUncertainReceipts(t *testing.T) {
	for _, mode := range []string{"foreign-author", "before-undo", "changed", "missing", "receipt-denied", "receipt-uncertain"} {
		t.Run(mode, func(t *testing.T) {
			f := pendingInverseFixture(t)
			writes := f.targetWrites
			switch mode {
			case "foreign-author":
				f.mode = "undo-actor"
			case "before-undo":
				a, err := decodeActionJournal(*f.comment, "gh-9")
				if err != nil {
					t.Fatal(err)
				}
				i := f.issues[1]
				i.Title, i.Body, i.State = a.After.Title, a.After.Body, a.After.State
				f.issues[1] = i
			case "changed":
				i := f.issues[1]
				i.Title = "Foreign edit"
				f.issues[1] = i
			case "missing":
				delete(f.issues, 1)
			default:
				f.mode = mode
			}
			result, err := f.client.ReconcileAction(context.Background(), "ghc-101", "ses_recovery", true)
			if result != nil || err == nil || f.targetWrites != writes {
				t.Fatal("unsafe recovery replayed/succeeded", result, err)
			}
			if mode == "receipt-uncertain" {
				f.mode = ""
				receipts := f.receiptWrites
				result, err = f.client.ReconcileAction(context.Background(), "ghc-101", "ses_recovery", true)
				if err != nil || !result.AlreadyReconciled || result.Applied || f.receiptWrites != receipts || f.targetWrites != writes {
					t.Fatal("saved recovery replayed", result, err)
				}
			}
		})
	}
}

func TestUndoReconcileRestoredReviewNeedsFreshProof(t *testing.T) {
	for _, mode := range []string{"", "events", "handoff", "events-denied", "target-after-final-review"} {
		t.Run(mode, func(t *testing.T) {
			f, setMode, _ := reviewTransitionUndoFixture(t, "reject")
			f.mode = "receipt-uncertain"
			if _, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor"); err == nil {
				t.Fatal("uncertain intent succeeded")
			}
			f.mode = ""
			// Intent failed before target: current still After, cannot claim inverse.
			result, err := f.client.ReconcileAction(context.Background(), "ghc-101", "ses_recovery", true)
			if result != nil || err == nil {
				t.Fatal("unapplied inverse accepted", result, err)
			}
			a, err := decodeActionJournal(*f.comment, "gh-9")
			if err != nil {
				t.Fatal(err)
			}
			i := f.issues[1]
			i.Title, i.Body, i.State = a.Before.Title, a.Before.Body, a.Before.State
			i.UpdatedAt = i.UpdatedAt.Add(time.Second)
			i.Labels = nil
			for _, name := range a.Before.Labels {
				i.Labels = append(i.Labels, struct {
					Name string `json:"name"`
				}{name})
			}
			f.issues[1] = i
			setMode(mode)
			if mode == "target-after-final-review" {
				base := f.client.run
				reads := 0
				f.client.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
					if strings.Contains(strings.Join(args, " "), "/issues/1/events?per_page=100") {
						reads++
						if reads == 3 {
							i := f.issues[1]
							i.Title = "Changed during final recovery proof"
							f.issues[1] = i
						}
					}
					return base(ctx, dir, payload, args...)
				}
			}
			writes := f.targetWrites
			result, err = f.client.ReconcileAction(context.Background(), "ghc-101", "ses_recovery", true)
			if mode == "" {
				if err != nil || !result.Applied {
					t.Fatal(result, err)
				}
			} else if result != nil || err == nil {
				t.Fatal("stale original proof accepted", result, err)
			}
			if mode == "events-denied" && !strings.Contains(err.Error(), "HTTP 403") {
				t.Fatal("cause lost", err)
			}
			if f.targetWrites != writes {
				t.Fatal("target replayed")
			}
		})
	}
}

func TestUndoReconcileCommentAbsenceDoesNotReplayDelete(t *testing.T) {
	for _, kind := range []string{"comment", "log", "handoff"} {
		t.Run(kind, func(t *testing.T) {
			f := prepareCommentUndoFixture(t, kind)
			f.journalWriteFixture.mode = "undo-receipt-denied"
			if _, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor"); err == nil {
				t.Fatal("lost receipt succeeded")
			}
			f.journalWriteFixture.mode = ""
			deletes := f.deletes
			result, err := f.client.ReconcileAction(context.Background(), "ghc-101", "ses_recovery", true)
			if err != nil || !result.Applied || result.Journal.UndoObservation == nil || f.deletes != deletes || result.UndoOutcome.TargetWriteVerified || result.UndoOutcome.CausalUndoChainEligible {
				t.Fatal(result, err)
			}
		})
	}
}

func TestUndoReconcileVersionAndActorCannotBeForged(t *testing.T) {
	f := pendingInverseFixture(t)
	result, err := f.client.ReconcileAction(context.Background(), "ghc-101", "ses_recovery", true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.comment.Body, actionUpdateIntentJournalStart) {
		t.Fatal("observation not versioned")
	}
	if result.Journal.UndoObservation.SessionID != "ses_recovery" || result.Journal.UndoObservation.Actor != "github-author" || result.UndoOutcome.TargetWriteVerified || result.UndoOutcome.CausalUndoChainEligible {
		t.Fatal("provenance lost", result)
	}
	for _, mode := range []string{"old-version", "foreign-author", "changed-state", "fake-proof"} {
		copy := *f.comment
		switch mode {
		case "old-version":
			copy.Body = strings.Replace(copy.Body, actionUpdateIntentJournalStart, actionJournalStart, 1)
		case "foreign-author":
			copy.User.Login = "other-author"
		case "changed-state", "fake-proof":
			a := result.Journal.ActionJournal
			o := *a.UndoObservation
			a.UndoObservation = &o
			if mode == "fake-proof" {
				o.Proof = "verified-write"
			} else {
				state := *o.State
				state.Title = "Foreign"
				state.Revision, _ = state.checksum()
				o.State = &state
			}
			if _, err := renderActionJournal(a); err == nil {
				t.Fatal("invalid observation rendered", mode)
			}
			continue
		}
		if _, err := decodeActionJournal(copy, "gh-9"); err == nil {
			t.Fatal("forged observation accepted", mode)
		}
	}
}

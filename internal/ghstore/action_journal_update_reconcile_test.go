package ghstore

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/models"
)

func pendingForwardUpdate(t *testing.T, mode string) *journalWriteFixture {
	t.Helper()
	f := newJournalWriteFixture(t, mode)
	if err := f.client.TrackActions("ses_actor"); err != nil {
		t.Fatal(err)
	}
	title := "Requested title"
	if _, err := f.client.Update(context.Background(), "gh-1", Changes{Title: &title}); err == nil && mode != "" {
		t.Fatal("fixture did not fail its original write")
	}
	f.mode = ""
	return f
}

func TestForwardUpdateRecoveryObservesWithoutReplayingTarget(t *testing.T) {
	for _, mode := range []string{"target-uncertain", "receipt-denied", "receipt-uncertain", ""} {
		t.Run(mode, func(t *testing.T) {
			f := pendingForwardUpdate(t, mode)
			original, err := decodeActionJournal(*f.comment, "gh-9")
			if err != nil {
				t.Fatal(err)
			}
			writes := f.targetWrites
			issue := f.issues[1]
			preview, err := f.client.ReconcileAction(context.Background(), "ghc-101", "ses_recovery", false)
			if err != nil || preview.Applied || preview.TargetWriteReplayed || f.targetWrites != writes {
				t.Fatal(preview, err)
			}
			existing := original.Phase == "applied"
			applied, err := f.client.ReconcileAction(context.Background(), "ghc-101", "ses_recovery", true)
			if err != nil || applied.Applied == existing || applied.AlreadyReconciled != existing || applied.TargetWriteReplayed || !reflect.DeepEqual(f.issues[1], issue) || f.targetWrites != writes {
				t.Fatal(applied, err)
			}
			if applied.ForwardOutcome.TargetWriteVerified != existing || applied.ForwardOutcome.CausalUndoChainEligible != existing {
				t.Fatal("observation promoted to original write proof", applied.ForwardOutcome)
			}
			if !reflect.DeepEqual(applied.Journal.Before, original.Before) || !reflect.DeepEqual(applied.Journal.UpdateIntent, original.UpdateIntent) || applied.Journal.SessionID != original.SessionID {
				t.Fatal("original intent rewritten")
			}
			if !existing {
				o := applied.Journal.UpdateObservation
				if o == nil || o.SessionID != "ses_recovery" || o.Actor != "github-author" || o.Proof != forwardObservationProof || !reflect.DeepEqual(o.State, applied.Journal.After) {
					t.Fatal("actual observation provenance lost", o)
				}
				if err := verifyUndoWitnesses([]ActionJournalRecord{*applied.Journal}, []ActionJournalRecord{*applied.Journal}); err == nil {
					t.Fatal("observed forward result became a causal undo witness")
				}
			}
			comment := *f.comment
			repeat, err := f.client.ReconcileAction(context.Background(), "ghc-101", "ses_recovery", true)
			if err != nil || repeat.Applied || !repeat.AlreadyReconciled || repeat.TargetWriteReplayed || !reflect.DeepEqual(f.issues[1], issue) || !reflect.DeepEqual(*f.comment, comment) {
				t.Fatal("repeat wrote target or journal", repeat, err)
			}
		})
	}
}

func TestForwardUpdateRecoveryRejectsUnappliedForeignOrStaleResults(t *testing.T) {
	for _, mode := range []string{"target-denied", "foreign-author", "changed-target", "old-clock", "legacy-input", "collection-denied", "changed-during-receipt-read"} {
		t.Run(mode, func(t *testing.T) {
			originalMode := "receipt-denied"
			if mode == "target-denied" {
				originalMode = mode
			}
			f := pendingForwardUpdate(t, originalMode)
			switch mode {
			case "foreign-author":
				f.mode = "undo-actor"
			case "changed-target":
				issue := f.issues[1]
				issue.Title = "Foreign edit"
				f.issues[1] = issue
			case "old-clock":
				issue := f.issues[1]
				issue.UpdatedAt = issue.UpdatedAt.Add(-24 * time.Hour)
				f.issues[1] = issue
			case "legacy-input":
				a, err := decodeActionJournal(*f.comment, "gh-9")
				if err != nil {
					t.Fatal(err)
				}
				a.UpdateIntent = nil
				f.comment.Body, err = renderActionJournal(a.ActionJournal)
				if err != nil {
					t.Fatal(err)
				}
			case "collection-denied", "changed-during-receipt-read":
				base := f.client.run
				reads := 0
				f.client.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
					if mode == "collection-denied" && strings.Contains(strings.Join(args, " "), "/issues?state=all") {
						return nil, errors.New("HTTP 403 forward collection denied")
					}
					data, err := base(ctx, dir, payload, args...)
					if args[4] == "GET" && strings.Contains(args[5], "/issues/comments/101") {
						reads++
						if reads == 2 {
							issue := f.issues[1]
							issue.Title = "Edit while rereading original receipt"
							f.issues[1] = issue
						}
					}
					return data, err
				}
			}
			writes, receipts := f.targetWrites, f.receiptWrites
			_, err := f.client.ReconcileAction(context.Background(), "ghc-101", "ses_recovery", true)
			if err == nil || f.targetWrites != writes || f.receiptWrites != receipts {
				t.Fatal("unsafe recovery wrote or succeeded", err)
			}
			if mode == "collection-denied" && !strings.Contains(err.Error(), "HTTP 403 forward collection denied") {
				t.Fatal("original API cause hidden", err)
			}
		})
	}
}

func TestForwardUpdateRecoveryPreservesNativeReviewAndEventProofs(t *testing.T) {
	for _, variant := range []string{"close-approved", "approve-new", "reopen", "close-unreviewed", "close-minor"} {
		for _, failure := range []string{"", "native-events-denied", "native-events-extra", "handoff"} {
			t.Run(variant+"/"+failure, func(t *testing.T) {
				f, setMode := nativeExecutionFixture(t, variant)
				a, err := decodeActionJournal(*f.comment, "gh-9")
				if err != nil {
					t.Fatal(err)
				}
				// Synthetic lost completion: keep the real planned input and target,
				// remove only the fixture's saved result.
				a.Phase, a.After = "prepared", nil
				f.comment.Body, err = renderActionJournal(a.ActionJournal)
				if err != nil {
					t.Fatal(err)
				}
				setMode(failure)
				writes := f.targetWrites
				out, err := f.client.ReconcileAction(context.Background(), "ghc-101", "ses_recovery", true)
				needsHandoff := variant == "close-approved" || variant == "approve-new"
				shouldFail := failure != "" && (failure != "handoff" || needsHandoff)
				if (err != nil) != shouldFail || f.targetWrites != writes {
					t.Fatal(out, err)
				}
				if !shouldFail && (out.Journal.UpdateObservation == nil || out.ForwardOutcome.TargetWriteVerified || out.TargetWriteReplayed) {
					t.Fatal("native observation falsely proved a write", out)
				}
			})
		}
	}
}

func TestForwardUpdateIntentCapturesGraphAndRejectsChangedRelatedTarget(t *testing.T) {
	f := newJournalWriteFixture(t, "receipt-denied")
	issue := f.issues[1]
	var err error
	issue.Body, err = encodeBody("Before", metadata{Type: models.TypeTask, Priority: models.PriorityP2, Details: &IssueDetails{Status: models.StatusOpen}})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"td:open", "td:priority:P2"} {
		issue.Labels = append(issue.Labels, struct {
			Name string `json:"name"`
		}{name})
	}
	f.issues[1] = issue
	dep := issue
	dep.Number, dep.Title = 2, "Dependency"
	f.issues[2] = dep
	if err := f.client.TrackActions("ses_actor"); err != nil {
		t.Fatal(err)
	}
	d := IssueDetails{Status: models.StatusOpen, Dependencies: []string{"gh-2"}}
	if _, err := f.client.Update(context.Background(), "gh-1", Changes{Details: &d}); err == nil {
		t.Fatal("fixture receipt failure absent")
	}
	f.mode = ""
	a, err := decodeActionJournal(*f.comment, "gh-9")
	if err != nil || len(a.UpdateIntent.Related) != 1 || a.UpdateIntent.Related[0].ID != "gh-2" {
		t.Fatal("graph evidence was not recorded before writing", a, err)
	}
	dep.Title = "Foreign related edit"
	f.issues[2] = dep
	_, err = f.client.ReconcileAction(context.Background(), "ghc-101", "ses_recovery", true)
	if err == nil {
		t.Fatal("changed related target was accepted")
	}
}

func TestForwardUpdateObservationCannotForgeActorOrInput(t *testing.T) {
	f := pendingForwardUpdate(t, "receipt-denied")
	out, err := f.client.ReconcileAction(context.Background(), "ghc-101", "ses_recovery", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"actor", "proof", "state", "original-input", "no-input"} {
		data, _ := json.Marshal(out.Journal.ActionJournal)
		var a ActionJournal
		if err := json.Unmarshal(data, &a); err != nil {
			t.Fatal(err)
		}
		comment := *f.comment
		switch mode {
		case "actor":
			comment.User.Login = "foreign-actor"
		case "proof":
			a.UpdateObservation.Proof = "original-write-verified"
		case "state":
			a.UpdateObservation.State.Title = "Unobserved result"
		case "original-input":
			a.UpdateIntent.Target.Title = "Different request"
		case "no-input":
			a.UpdateIntent = nil
		}
		comment.Body, err = renderActionJournal(a)
		if err != nil {
			continue
		}
		if _, err := decodeActionJournal(comment, "gh-9"); err == nil {
			t.Fatal("forged observation accepted", mode)
		}
	}
}

func TestForwardUpdateRecoveryReceiptFailuresStayUncertainAndNeverReplay(t *testing.T) {
	for _, mode := range []string{"receipt-denied", "receipt-uncertain", "source-after-receipt"} {
		t.Run(mode, func(t *testing.T) {
			f := pendingForwardUpdate(t, "receipt-denied")
			if mode == "source-after-receipt" {
				base := f.client.run
				f.client.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
					data, err := base(ctx, dir, payload, args...)
					if args[4] == "PATCH" && strings.Contains(args[5], "/issues/comments/101") {
						issue := f.issues[1]
						issue.Title = "Foreign edit after observation receipt"
						f.issues[1] = issue
					}
					return data, err
				}
			} else {
				f.mode = mode
			}
			writes := f.targetWrites
			_, err := f.client.ReconcileAction(context.Background(), "ghc-101", "ses_recovery", true)
			if err == nil || f.targetWrites != writes {
				t.Fatal("recovery failure hidden or target replayed", err)
			}
			if mode == "source-after-receipt" {
				return
			}
			f.mode = ""
			out, err := f.client.ReconcileAction(context.Background(), "ghc-101", "ses_recovery", true)
			if err != nil || f.targetWrites != writes || out.ForwardOutcome.TargetWriteVerified {
				t.Fatal("explicit reobservation failed or claimed original write", out, err)
			}
		})
	}
}

func TestForwardObservedSourceInverseIsVerifiedButNotCausal(t *testing.T) {
	f := pendingForwardUpdate(t, "receipt-denied")
	if _, err := f.client.ReconcileAction(context.Background(), "ghc-101", "ses_recovery", true); err != nil {
		t.Fatal(err)
	}
	writes := f.targetWrites
	out, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor")
	if err != nil || out.UndoOutcome == nil || !out.UndoOutcome.TargetWriteVerified || out.UndoOutcome.CausalUndoChainEligible || f.targetWrites != writes+1 {
		t.Fatal(out, err)
	}
	if err := verifyUndoWitnesses([]ActionJournalRecord{*out.Journal}, []ActionJournalRecord{*out.Journal}); err == nil {
		t.Fatal("observed original became a causal inverse witness")
	}
	before := *f.comment
	repeat, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor")
	if err != nil || !repeat.AlreadyUndone || repeat.UndoOutcome.CausalUndoChainEligible || f.targetWrites != writes+1 || !reflect.DeepEqual(before, *f.comment) {
		t.Fatal("completed observed-source inverse was replayed", repeat, err)
	}
}

func TestForwardAuxiliaryUpdateRecoveryAndBoardTargetProof(t *testing.T) {
	for _, kind := range []string{"note", "board"} {
		t.Run(kind, func(t *testing.T) {
			f := newJournalWriteFixture(t, "")
			var note *NoteRecord
			var board *BoardRecord
			var err error
			if kind == "note" {
				note, err = f.client.CreateNote(context.Background(), "Forward note", "Original", "ses_actor")
			} else {
				board, err = f.client.CreateBoard(context.Background(), "Forward board", "status = open", "ses_actor")
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := f.client.TrackActions("ses_actor"); err != nil {
				t.Fatal(err)
			}
			f.mode = "receipt-denied"
			if kind == "note" {
				title := "Requested note"
				_, _, err = f.client.UpdateNoteObserved(context.Background(), note, NoteChanges{Title: &title}, "ses_actor")
			} else {
				_, err = f.client.SetBoardPositionObserved(context.Background(), board, "gh-1", 1, "ses_actor")
			}
			if err == nil {
				t.Fatal("fixture did not lose its completion")
			}
			f.mode = ""
			a, err := decodeActionJournal(*f.comment, "gh-9")
			if err != nil || a.UpdateIntent == nil {
				t.Fatal(a, err)
			}
			if kind == "board" && (len(a.UpdateIntent.Related) != 1 || a.UpdateIntent.Related[0].ID != "gh-1") {
				t.Fatal("moved task membership was not recorded", a.UpdateIntent)
			}
			writes := f.targetWrites
			out, err := f.client.ReconcileAction(context.Background(), "ghc-101", "ses_recovery", true)
			if err != nil || !out.Applied || out.ForwardOutcome.TargetWriteVerified || f.targetWrites != writes {
				t.Fatal(out, err)
			}
			if kind == "board" {
				issue := f.issues[1]
				issue.Title = "Foreign position-target edit"
				f.issues[1] = issue
				if _, err := f.client.ReconcileAction(context.Background(), "ghc-101", "ses_recovery", true); err == nil {
					t.Fatal("changed moved task was accepted")
				}
			}
		})
	}
}

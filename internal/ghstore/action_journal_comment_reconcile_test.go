package ghstore

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/models"
)

func TestCommentForwardRecoveryDoesNotReplayCreation(t *testing.T) {
	f := newCommentWriteFixture(t)
	f.mode = "lost"
	if _, err := f.client.AppendActivity(context.Background(), "gh-1", models.Activity{
		Kind: "log", SessionID: "ses_actor", OperationID: "shared-activity", Message: "Saved once", LogType: models.LogTypeProgress,
	}); err == nil {
		t.Fatal("lost response unexpectedly succeeded")
	}
	f.mode = ""
	before, comment, body := f.issues[1], f.native[0], f.comment.Body
	preview, err := f.client.ReconcileAction(context.Background(), "ghc-101", "ses_observer", false)
	if err != nil {
		t.Fatal("created comment cannot be observed without replay", err)
	}
	if preview.Applied || preview.AlreadyReconciled || f.comment.Body != body || f.writes != 1 {
		t.Fatal("preview changed the receipt or repeated creation", preview)
	}
	result, err := f.client.ReconcileAction(context.Background(), "ghc-101", "ses_observer", true)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || result.Journal.Phase != "applied" || result.Journal.SessionID != "ses_actor" || result.Journal.CommentAfter.ID != "ghc-201" || result.ForwardOutcome == nil || result.ForwardOutcome.TargetWriteVerified || result.ForwardOutcome.CausalUndoChainEligible {
		t.Fatal("recovery fabricated original attribution or verified proof", result)
	}
	if !reflect.DeepEqual(before, f.issues[1]) || !reflect.DeepEqual(comment, f.native[0]) || f.writes != 1 || f.deletes != 0 {
		t.Fatal("receipt recovery modified the native target")
	}
	completed := *f.comment
	confirmed, err := f.client.ReconcileAction(context.Background(), "ghc-101", "ses_observer", true)
	if err != nil || !confirmed.AlreadyReconciled || confirmed.Applied || !reflect.DeepEqual(completed, *f.comment) {
		t.Fatal("completed confirmation rewrote the receipt", confirmed, err)
	}
	undone, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor")
	if err != nil || undone.Journal.Phase != "undone" || f.writes != 1 || f.deletes != 1 || undone.UndoOutcome == nil || undone.UndoOutcome.CausalUndoChainEligible {
		t.Fatal("recovered creation is not undoable with honest proof", undone, err)
	}
	if err := verifyUndoWitnesses([]ActionJournalRecord{*undone.Journal}, []ActionJournalRecord{*undone.Journal}); err == nil {
		t.Fatal("verified DELETE promoted an observed creation into a causal witness")
	}
}

func TestCommentForwardRecoveryRejectsMissingChangedOrAmbiguousEvidence(t *testing.T) {
	for _, mode := range []string{"missing", "duplicate", "author", "edited", "body", "target", "predates", "point-conflict"} {
		t.Run(mode, func(t *testing.T) {
			f := newCommentWriteFixture(t)
			f.mode = "lost"
			if _, err := f.client.AppendActivity(context.Background(), "gh-1", models.Activity{Kind: "comment", SessionID: "ses_actor", Message: "Original"}); err == nil {
				t.Fatal("expected lost response")
			}
			f.mode = ""
			switch mode {
			case "missing":
				f.native = nil
			case "duplicate":
				f.mode = "duplicate"
			case "author":
				f.native[0].User.Login = "another-author"
			case "edited":
				f.native[0].UpdatedAt = f.native[0].UpdatedAt.Add(time.Second)
			case "body":
				f.native[0].Body = strings.ReplaceAll(f.native[0].Body, "Original", "Changed")
			case "target":
				issue := f.issues[1]
				issue.Title = "Native edit"
				f.issues[1] = issue
			case "predates":
				f.native[0].CreatedAt = f.comment.CreatedAt.Add(-time.Second)
				f.native[0].UpdatedAt = f.native[0].CreatedAt
			case "point-conflict":
				f.mode = "readback-changed"
			}
			original, writes := *f.comment, f.receiptWrites
			if _, err := f.client.ReconcileAction(context.Background(), "ghc-101", "ses_observer", true); err == nil {
				t.Fatal("ambiguous evidence was accepted")
			}
			if f.receiptWrites != writes || !reflect.DeepEqual(original, *f.comment) || f.writes != 1 || f.deletes != 0 || f.targetWrites != 0 {
				t.Fatal("failed recovery mutated target or original receipt")
			}
		})
	}
}

func TestCommentForwardRecoveryLostReceiptKeepsOriginalObserver(t *testing.T) {
	f := newCommentWriteFixture(t)
	f.mode = "lost"
	if _, err := f.client.AppendActivity(context.Background(), "gh-1", models.Activity{Kind: "comment", SessionID: "ses_actor", Message: "Once"}); err == nil {
		t.Fatal("expected uncertain creation")
	}
	f.mode, f.journalWriteFixture.mode = "", "receipt-uncertain"
	if _, err := f.client.ReconcileAction(context.Background(), "ghc-101", "ses_observer", true); err == nil {
		t.Fatal("lost recovery response reported success")
	}
	f.journalWriteFixture.mode = ""
	saved, writes := *f.comment, f.receiptWrites
	result, err := f.client.ReconcileAction(context.Background(), "ghc-101", "ses_later_observer", true)
	if err != nil || !result.AlreadyReconciled || result.Applied || result.Journal.CommentObservation.SessionID != "ses_observer" || result.ForwardOutcome.Proof != commentObservationProof {
		t.Fatal(result, err)
	}
	if !reflect.DeepEqual(saved, *f.comment) || f.receiptWrites != writes || f.writes != 1 {
		t.Fatal("lost receipt confirmation replayed a write")
	}
}

func TestCommentObservationVersionAndInverseRecovery(t *testing.T) {
	f := newCommentWriteFixture(t)
	f.mode = "lost"
	if _, err := f.client.AppendActivity(context.Background(), "gh-1", models.Activity{Kind: "handoff", SessionID: "ses_actor", Done: []string{"Checkpoint"}}); err == nil {
		t.Fatal("expected uncertain creation")
	}
	f.mode = ""
	observed, err := f.client.ReconcileAction(context.Background(), "ghc-101", "ses_observer", true)
	if err != nil {
		t.Fatal(err)
	}
	if observed.Journal.CommentObservation == nil || !strings.Contains(f.comment.Body, actionCommentObservedJournalStart) {
		t.Fatal("missing versioned observer")
	}
	wrongVersion := *f.comment
	wrongVersion.Body = strings.Replace(wrongVersion.Body, actionCommentObservedJournalStart, actionCommentJournalStart, 1)
	if _, err := decodeActionJournal(wrongVersion, "gh-9"); err == nil {
		t.Fatal("legacy v2 accepted later observer fields")
	}
	wrongActor := *f.comment
	wrongActor.User.Login = "foreign"
	if _, err := decodeActionJournal(wrongActor, "gh-9"); err == nil {
		t.Fatal("observer author was not fenced")
	}
	f.mode = "delete-lost"
	if _, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor"); err == nil {
		t.Fatal("lost DELETE reported success")
	}
	f.mode = ""
	result, err := f.client.ReconcileAction(context.Background(), "ghc-101", "ses_inverse_observer", true)
	if err != nil || result.Journal.CommentObservation.SessionID != "ses_observer" || result.Journal.UndoObservation.SessionID != "ses_inverse_observer" || result.UndoOutcome.TargetWriteVerified || result.UndoOutcome.CausalUndoChainEligible || f.deletes != 1 {
		t.Fatal("inverse recovery lost provenance or replayed DELETE", result, err)
	}
	if err := verifyUndoWitnesses([]ActionJournalRecord{*result.Journal}, []ActionJournalRecord{*result.Journal}); err == nil {
		t.Fatal("observed forward/inverse became a causal undo witness")
	}
}

func TestCommentObservationCompressedRoundTripAndValidation(t *testing.T) {
	f := newCommentWriteFixture(t)
	f.mode = "lost"
	if _, err := f.client.AppendActivity(context.Background(), "gh-1", models.Activity{Kind: "comment", SessionID: "ses_actor", Message: "Saved"}); err == nil {
		t.Fatal("expected lost response")
	}
	f.mode = ""
	result, err := f.client.ReconcileAction(context.Background(), "ghc-101", "ses_observer", true)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result.Journal.ActionJournal)
	if err != nil {
		t.Fatal(err)
	}
	copyAction := func() ActionJournal {
		t.Helper()
		var a ActionJournal
		if err := json.Unmarshal(encoded, &a); err != nil {
			t.Fatal(err)
		}
		return a
	}
	for name, change := range map[string]func(*ActionJournal){
		"proof":    func(a *ActionJournal) { a.CommentObservation.Proof = "verified-forward-receipt" },
		"actor":    func(a *ActionJournal) { a.CommentObservation.Actor = "" },
		"session":  func(a *ActionJournal) { a.CommentObservation.SessionID = "invalid session" },
		"state":    func(a *ActionJournal) { a.CommentObservation.State = a.Before },
		"prepared": func(a *ActionJournal) { a.Phase = "prepared" },
	} {
		t.Run(name, func(t *testing.T) {
			a := copyAction()
			change(&a)
			if _, err := renderWriteActionJournal(a); err == nil {
				t.Fatal("invalid observer was accepted")
			}
		})
	}
	a := copyAction()
	for _, state := range []*ActionIssueState{a.Before, a.After, a.CommentObservation.State} {
		state.Body = strings.Repeat("Large original issue description\n", 4000)
		state.Revision, err = state.checksum()
		if err != nil {
			t.Fatal(err)
		}
	}
	body, err := renderWriteActionJournal(a)
	if err != nil || !strings.Contains(body, actionCompressedJournalStart) {
		t.Fatal("v18 compression", err)
	}
	native := *f.comment
	native.Body = body
	roundTrip, err := decodeActionJournal(native, "gh-9")
	if err != nil || !reflect.DeepEqual(roundTrip.ActionJournal, a) {
		t.Fatal("compressed v18 cannot retain observer", err)
	}
}

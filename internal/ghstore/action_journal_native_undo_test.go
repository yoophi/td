package ghstore

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/marcus/td/internal/reviewpolicy"
)

func nativeExecutionFixture(t *testing.T, variant string) (*journalWriteFixture, func(string)) {
	t.Helper()
	f, setFileMode := fileUndoFixture(t, variant != "close-unreviewed" && variant != "close-minor", variant == "reopen")
	if _, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor"); err != nil {
		t.Fatal(err)
	}
	events := []nativeStateEvent{}
	if variant == "reopen" {
		events = append(events, nativeStateEvent{ID: 1, Event: "closed"})
	}
	mode := ""
	var delayed *nativeStateEvent
	base := f.client.run
	f.client.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "/issues/1/events?per_page=100") {
			if mode == "native-events-denied" {
				return nil, errors.New("HTTP 403 native events denied")
			}
			result := slices.Clone(events)
			if mode == "native-events-extra" || mode == "native-event-after-intent" && strings.Contains(f.comment.Body, `"phase":"undo_prepared"`) || mode == "native-event-after-receipt" && strings.Contains(f.comment.Body, `"phase":"undone"`) {
				result = append(result, nativeStateEvent{ID: int64(len(events) + 100), Event: "reopened"})
			}
			return json.Marshal([][]nativeStateEvent{result})
		}
		old := f.issues[1].State
		data, err := base(ctx, dir, payload, args...)
		if slices.Contains(args, "PATCH") && slices.Contains(args, "repos/owner/repo/issues/1") && old != f.issues[1].State {
			event := "closed"
			if f.issues[1].State == "open" {
				event = "reopened"
			}
			next := nativeStateEvent{ID: int64(len(events) + 1), Event: event}
			if mode == "late-event" {
				delayed = &next
			} else {
				events = append(events, next)
			}
		}
		if mode == "target-after-receipt-read" && slices.Contains(args, "GET") && strings.Contains(joined, "/issues/comments/101") && strings.Contains(f.comment.Body, `"phase":"undo_prepared"`) && strings.Contains(f.issues[1].Body, `"action":"undo_native"`) {
			issue := f.issues[1]
			issue.Title = "Foreign edit during original receipt read"
			f.issues[1] = issue
		}
		return data, err
	}
	if variant == "approve-new" {
		issue := f.issues[1]
		desc, m, _, _ := decodeBody(issue.Body)
		m.Details.Reviews = nil
		m.Details.ReviewerSession = ""
		m.Details.ReviewedAt = nil
		issue.Body, _ = encodeBody(desc, m)
		f.issues[1] = issue
	}
	if variant == "close-minor" {
		if _, _, err := f.client.Transition(context.Background(), "gh-1", "review", TransitionOptions{SessionID: "ses_actor", Mode: reviewpolicy.ModeTrusted, Minor: true, Reason: "Synthetic optional minor review"}); err != nil {
			t.Fatal(err)
		}
	}
	action := "close"
	if variant == "reopen" {
		action = "reopen"
	}
	if strings.HasPrefix(variant, "approve-") {
		action = "approve"
	}
	o := TransitionOptions{SessionID: "ses_actor", Mode: reviewpolicy.ModeTrusted, Reason: "Synthetic native transition"}
	if variant == "close-unreviewed" {
		o.AdminReason = "Synthetic explicit closure"
	}
	if _, _, err := f.client.Transition(context.Background(), "gh-1", action, o); err != nil {
		t.Fatal(err)
	}
	f.commentReads = 0
	f.receiptWrites = 1
	return f, func(value string) {
		mode = value
		if value == "handoff" {
			setFileMode("handoff")
		} else {
			setFileMode("")
		}
		if value == "" && delayed != nil {
			events = append(events, *delayed)
			delayed = nil
		}
	}
}

func TestNativeUndoExecutesCompensationWithoutRevivingApproval(t *testing.T) {
	for _, variant := range []string{"close-unreviewed", "close-minor", "close-approved", "approve-new", "approve-existing", "reopen"} {
		t.Run(variant, func(t *testing.T) {
			f, _ := nativeExecutionFixture(t, variant)
			original, err := decodeActionJournal(*f.comment, "gh-9")
			if err != nil {
				t.Fatal(err)
			}
			writes := f.targetWrites
			result, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor")
			if err != nil || result.Journal.Phase != "undone" || result.Compensation == nil || !result.UndoOutcome.TargetWriteVerified || result.UndoOutcome.CausalUndoChainEligible || f.targetWrites != writes+1 {
				t.Fatal(result, err)
			}
			if result.Issue.nativeState != original.Before.State || activeApproval(result.Issue.Details) != nil || result.Issue.ReviewedAt != nil || result.Issue.ReviewerSession != "" {
				t.Fatal("approval incorrectly revived", result)
			}
			if !reflect.DeepEqual(original.Before, result.Journal.Before) || !reflect.DeepEqual(original.After, result.Journal.After) {
				t.Fatal("original snapshots changed")
			}
			if variant == "close-minor" && result.Compensation.RequiresNewReview {
				t.Fatal("minor inverse incorrectly requires review")
			}
			if !strings.Contains(f.comment.Body, actionNativeUndoJournalStart) {
				t.Fatal("native result not versioned")
			}
			writes, receipts := f.targetWrites, f.receiptWrites
			result, err = f.client.UndoAction(context.Background(), "ghc-101", "ses_actor")
			if err != nil || !result.AlreadyUndone || f.targetWrites != writes || f.receiptWrites != receipts {
				t.Fatal("repeat wrote", result, err)
			}
			recovery, err := f.client.ReconcileAction(context.Background(), "ghc-101", "ses_recovery", true)
			if err != nil || !recovery.AlreadyReconciled || recovery.Applied || recovery.TargetWriteReplayed || !recovery.UndoOutcome.TargetWriteVerified || recovery.UndoOutcome.CausalUndoChainEligible || f.targetWrites != writes || f.receiptWrites != receipts {
				t.Fatal("completion confirmation wrote", recovery, err)
			}
		})
	}
}

func TestNativeUndoFailureBoundariesNeverReplay(t *testing.T) {
	for _, mode := range []string{"undo-actor", "target-conflict", "target-denied", "target-uncertain", "receipt-denied", "receipt-uncertain", "undo-receipt-denied", "undo-receipt-uncertain", "journal-concurrent", "native-events-extra", "native-events-denied", "native-event-after-intent", "native-event-after-receipt", "handoff", "target-after-receipt-read"} {
		t.Run(mode, func(t *testing.T) {
			f, setMode := nativeExecutionFixture(t, "close-approved")
			setMode(mode)
			if strings.HasPrefix(mode, "native-") || mode == "handoff" || mode == "target-after-receipt-read" {
				f.mode = ""
			} else {
				f.mode = mode
			}
			before := f.targetWrites
			out, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor")
			if out != nil || err == nil || f.targetWrites > before+1 {
				t.Fatal("unsafe inverse succeeded/replayed", out, err)
			}
			if mode == "native-events-denied" && !strings.Contains(err.Error(), "HTTP 403") {
				t.Fatal("cause lost", err)
			}
			if mode == "target-after-receipt-read" && !strings.Contains(f.comment.Body, `"phase":"undo_prepared"`) {
				t.Fatal("completion stored despite final source conflict")
			}
		})
	}
}

func TestNativeUndoUncertainResultObservedWithoutTargetReplay(t *testing.T) {
	for _, mode := range []string{"undo-receipt-denied", "undo-receipt-uncertain", "target-uncertain", "late-event"} {
		t.Run(mode, func(t *testing.T) {
			f, setMode := nativeExecutionFixture(t, "approve-new")
			setMode(mode)
			if mode != "late-event" {
				f.mode = mode
			}
			if _, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor"); err == nil {
				t.Fatal("uncertain result succeeded")
			}
			f.mode = ""
			setMode("")
			writes, receipts := f.targetWrites, f.receiptWrites
			preview, err := f.client.ReconcileAction(context.Background(), "ghc-101", "ses_recovery", false)
			if err != nil || preview.Applied || preview.TargetWriteReplayed || f.targetWrites != writes || f.receiptWrites != receipts {
				t.Fatal(preview, err)
			}
			done, err := f.client.ReconcileAction(context.Background(), "ghc-101", "ses_recovery", true)
			if err != nil || done.TargetWriteReplayed || f.targetWrites != writes || done.UndoOutcome.CausalUndoChainEligible {
				t.Fatal(done, err)
			}
			// A receipt accepted despite lost response is genuinely complete and read-only.
			if mode == "undo-receipt-uncertain" {
				if !done.AlreadyReconciled || done.Applied || !done.UndoOutcome.TargetWriteVerified || f.receiptWrites != receipts {
					t.Fatal(done)
				}
			} else {
				if !done.Applied || done.Journal.NativeUndo.Proof != inverseObservationProof || done.UndoOutcome.TargetWriteVerified || done.Journal.NativeUndo.ObserverSession != "ses_recovery" {
					t.Fatal(done)
				}
			}
			receipts = f.receiptWrites
			repeat, err := f.client.ReconcileAction(context.Background(), "ghc-101", "ses_different_recovery", true)
			if err != nil || !repeat.AlreadyReconciled || repeat.Applied || f.targetWrites != writes || f.receiptWrites != receipts {
				t.Fatal("repeat replayed", repeat, err)
			}
		})
	}
}

func TestNativeUndoJournalCannotForgePlanVersionActorOrProof(t *testing.T) {
	f, _ := nativeExecutionFixture(t, "close-approved")
	result, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor")
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"version", "actor", "payload", "baseline", "events", "proof", "causal-policy"} {
		t.Run(mode, func(t *testing.T) {
			copy := *f.comment
			switch mode {
			case "version":
				copy.Body = strings.Replace(copy.Body, actionNativeUndoJournalStart, actionJournalStart, 1)
			case "actor":
				copy.User.Login = "foreign-author"
			default:
				data, _ := json.Marshal(result.Journal.ActionJournal)
				var action ActionJournal
				if err := json.Unmarshal(data, &action); err != nil {
					t.Fatal(err)
				}
				n := action.NativeUndo
				switch mode {
				case "payload":
					n.Plan.Payload["title"] = "Forged title"
				case "baseline":
					n.Baseline = nil
				case "events":
					n.EventsAfter = nil
				case "proof":
					n.Proof = "verified-approval"
				case "causal-policy":
					n.Plan.CausalUndoChainEligible = true
				}
				if _, err := renderActionJournal(action); err == nil {
					t.Fatal("forged native policy rendered", mode)
				}
				return
			}
			if _, err := decodeActionJournal(copy, "gh-9"); err == nil {
				t.Fatal("forged native codec accepted", mode)
			}
		})
	}
}

func TestNativeUndoRecoveryRejectsUnappliedInverse(t *testing.T) {
	for _, mode := range []string{"target-denied", "receipt-uncertain"} {
		t.Run(mode, func(t *testing.T) {
			f, _ := nativeExecutionFixture(t, "close-approved")
			f.mode = mode
			if _, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor"); err == nil {
				t.Fatal("injected failure succeeded")
			}
			f.mode = ""
			writes, receipts := f.targetWrites, f.receiptWrites
			out, err := f.client.ReconcileAction(context.Background(), "ghc-101", "ses_recovery", true)
			if out != nil || err == nil || f.targetWrites != writes || f.receiptWrites != receipts {
				t.Fatal("unapplied inverse accepted/replayed", out, err)
			}
		})
	}
}

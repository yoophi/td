package ghstore

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/marcus/td/internal/models"
)

func workflowUndoFixture(t *testing.T, action string, reviewed bool) (*journalWriteFixture, func(string), string) {
	t.Helper()
	f, mode := fileUndoFixture(t, reviewed, false)
	if _, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor"); err != nil {
		t.Fatal(err)
	}
	if !reviewed {
		issue := f.issues[1]
		desc, meta, _, err := decodeBody(issue.Body)
		if err != nil {
			t.Fatal(err)
		}
		switch action {
		case "unstart", "block":
			meta.Details.Status, meta.Details.ImplementerSession = models.StatusInProgress, "ses_actor"
		case "unblock", "start-blocked":
			meta.Details.Status, meta.Details.ImplementerSession = models.StatusBlocked, "ses_actor"
		}
		issue.Body, err = encodeBody(desc, meta)
		if err != nil {
			t.Fatal(err)
		}
		f.issues[1] = issue
	}
	if action == "unstart-open-claim" {
		issue := f.issues[1]
		desc, meta, _, _ := decodeBody(issue.Body)
		meta.Details.Status, meta.Details.ImplementerSession = models.StatusOpen, "ses_actor"
		issue.Body, _ = encodeBody(desc, meta)
		f.issues[1] = issue
	}
	original := f.issues[1].Body
	if action == "start-blocked" {
		action = "start"
	}
	if action == "unstart-open-claim" {
		action = "unstart"
	}
	_, _, err := f.client.Transition(context.Background(), "gh-1", action, TransitionOptions{SessionID: "ses_actor", Reason: "Fixture transition", Force: true})
	if err != nil {
		t.Fatal(err)
	}
	mode("")
	return f, mode, original
}

func TestWorkflowUndoRestoresOpenNativeTransitions(t *testing.T) {
	for _, action := range []string{"start", "unstart", "block", "unblock", "start-blocked", "unstart-open-claim"} {
		t.Run(action, func(t *testing.T) {
			f, _, original := workflowUndoFixture(t, action, false)
			a, err := decodeActionJournal(*f.comment, "gh-9")
			if err != nil {
				t.Fatal(err)
			}
			before, after := *a.Before, *a.After
			writes := f.targetWrites
			result, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor")
			if err != nil || result.Issue.nativeBody != original || result.Journal.Phase != "undone" || f.targetWrites != writes+1 {
				t.Fatal(result, err)
			}
			if !reflect.DeepEqual(before, *result.Journal.Before) || !reflect.DeepEqual(after, *result.Journal.After) {
				t.Fatal("original snapshots changed")
			}
			writes, receipts := f.targetWrites, f.receiptWrites
			result, err = f.client.UndoAction(context.Background(), "ghc-101", "ses_actor")
			if err != nil || !result.AlreadyUndone || f.targetWrites != writes || f.receiptWrites != receipts {
				t.Fatal("repeat wrote", result, err)
			}
		})
	}
}

func TestWorkflowUndoRestoresOriginalReviewOnlyWithFreshEvidence(t *testing.T) {
	for _, mode := range []string{"", "events", "handoff", "events-after-intent", "handoff-after-patch", "events-after-receipt", "target-after-final-review"} {
		t.Run(mode, func(t *testing.T) {
			f, setMode, original := workflowUndoFixture(t, "start", true)
			setMode(mode)
			result, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor")
			if mode == "" {
				if err != nil || result.Issue.nativeBody != original || activeApproval(result.Issue.Details) == nil {
					t.Fatal(result, err)
				}
			} else {
				var conflict *ConflictError
				if result != nil || !errors.As(err, &conflict) {
					t.Fatal("stale review restored", result, err)
				}
			}
		})
	}
}

func TestWorkflowUndoRejectsUnrelatedOrForgedEffects(t *testing.T) {
	for _, mode := range []string{"claim", "session-author", "session-history", "transition-author", "transition-history", "dependency", "title", "review-row", "native-state", "reason", "deleted"} {
		t.Run(mode, func(t *testing.T) {
			f, _, _ := workflowUndoFixture(t, "start", false)
			a, err := decodeActionJournal(*f.comment, "gh-9")
			if err != nil {
				t.Fatal(err)
			}
			_, meta, _, _ := decodeBody(a.After.Body)
			switch mode {
			case "claim":
				meta.Details.ImplementerSession = "ses_foreign"
			case "session-author":
				meta.Details.Sessions[len(meta.Details.Sessions)-1].SessionID = "ses_foreign"
			case "session-history":
				meta.Details.Sessions = nil
			case "transition-author":
				meta.Details.Transitions[len(meta.Details.Transitions)-1].SessionID = "ses_foreign"
			case "transition-history":
				meta.Details.Transitions = nil
			case "dependency":
				meta.Details.Dependencies = []string{"gh-2"}
			case "title":
				a.After.Title = "Foreign title"
			case "review-row":
				meta.Details.Reviews = []models.IssueReview{{ID: "fabricated", ReviewerSession: "ses_foreign", Decision: "approved", CreatedAt: a.After.UpdatedAt}}
			case "native-state":
				a.Before.State = "closed"
			case "reason":
				meta.LastStateReason = "Not transition reason"
			case "deleted":
				meta.Details.DeletedAt = &a.After.UpdatedAt
			}
			a.After.Body, err = encodeBody("Before", meta)
			if err != nil {
				t.Fatal(err)
			}
			// Check the scope independently of receipt checksum validation.
			if err = scalarUndoScope(a); err == nil {
				data, _ := json.Marshal(a)
				t.Fatal("forged effect accepted", mode, string(data))
			}
		})
	}
}

func TestWorkflowUndoFailureBoundariesNeverReplay(t *testing.T) {
	for _, mode := range []string{"undo-actor", "target-conflict", "target-denied", "target-uncertain", "receipt-denied", "receipt-uncertain", "journal-concurrent", "undo-later"} {
		t.Run(mode, func(t *testing.T) {
			f, _, _ := workflowUndoFixture(t, "start", false)
			f.mode = mode
			f.commentReads = 0
			writes := f.targetWrites
			result, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor")
			if result != nil || err == nil || f.targetWrites > writes+1 {
				t.Fatal("failed inverse succeeded or replayed", result, err, f.targetWrites)
			}
		})
	}
}

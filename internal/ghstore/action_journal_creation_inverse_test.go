package ghstore

import (
	"reflect"
	"testing"
	"time"
)

func creationInverseFixture(t *testing.T) (ActionJournal, ActionJournal, ActionJournal, ActionJournal) {
	t.Helper()
	plan, err := prepareIssueCreationInput(journalCreationFixtureIssue(), "td-op-created", []string{})
	if err != nil {
		t.Fatal(err)
	}
	after := journalStateFixture(t, plan.input.Title)
	after.Body, after.Labels = plan.input.Body, plan.input.Labels
	after.Revision, _ = after.checksum()
	evidence := &CreationObservation{Proof: "verified", SessionID: "ses_actor", Actor: "github-author", ObservedAt: after.UpdatedAt}
	base := ActionJournal{OperationID: "td-op-created", SessionID: "ses_actor", IssueID: "gh-2", CarrierID: "gh-9", Action: "create", Phase: "applied", CreateInput: &plan.input, After: &after,
		CreationCommand: &ActionCreationCommand{DependsOn: []string{}, Blocks: []string{}, OriginalIDs: []string{"gh-1"}, Watches: []ActionRelatedObservation{}, Events: map[string][]nativeStateEvent{}, RelationOperationID: "td-op-related", Allocation: evidence, Forward: evidence}}
	intent, _ := copyCreationJournal(base)
	description, meta, _, _ := decodeBody(after.Body)
	if meta.Details == nil {
		meta.Details = &IssueDetails{Status: "open"}
	}
	now := after.UpdatedAt.Add(time.Second)
	meta.Details.DeletedAt = &now
	meta.Details.Transitions = append(meta.Details.Transitions, TransitionRecord{OperationID: "td-op-delete", Action: "delete", From: "open", To: "open", SessionID: base.SessionID, At: now})
	body, err := encodeBody(description, meta)
	if err != nil {
		t.Fatal(err)
	}
	intent.Phase, intent.UndoOperationID = "undo_prepared", "td-op-undo"
	intent.CreationCommand.Inverse = &ActionCreationInverse{DeleteInput: ActionUpdateInput{Title: after.Title, Body: body, State: after.State, Labels: after.Labels}}
	checkpoint, _ := copyCreationJournal(intent)
	checkpoint.CreationCommand.Inverse.Checkpoint = &CreationInverseCheckpoint{Receipts: []*ActionJournalRecord{}}
	done, _ := copyCreationJournal(checkpoint)
	state := after
	state.Body, state.UpdatedAt = body, now
	state.Revision, _ = state.checksum()
	done.Phase, done.Restored = "undone", &state
	done.CreationCommand.Inverse.Result = evidence
	for _, a := range []ActionJournal{base, intent, checkpoint, done} {
		if err := a.validate(); err != nil {
			t.Fatal(err)
		}
	}
	return base, intent, checkpoint, done
}

func TestCreationInverseIntentCheckpointAndResultContracts(t *testing.T) {
	base, intent, checkpoint, done := creationInverseFixture(t)
	for _, pair := range [][2]ActionJournal{{base, intent}, {intent, checkpoint}, {checkpoint, done}} {
		if err := validateCreationInverseRewrite(pair[0], pair[1]); err != nil {
			t.Fatal(err)
		}
		comment := journalCommentFixture(t, pair[1])
		roundtrip, err := decodeActionJournal(comment, pair[1].CarrierID)
		if err != nil || !reflect.DeepEqual(roundtrip.ActionJournal, pair[1]) {
			t.Fatal("inverse roundtrip", err)
		}
	}
	for _, pair := range [][2]ActionJournal{{base, checkpoint}, {intent, done}, {done, checkpoint}, {checkpoint, intent}} {
		if err := validateCreationInverseRewrite(pair[0], pair[1]); err == nil {
			t.Fatal("skipped or regressed inverse boundary")
		}
	}
	for _, mode := range []string{"input", "original", "checkpoint", "proof", "author", "clock", "creation-owner"} {
		t.Run(mode, func(t *testing.T) {
			a, _ := copyCreationJournal(done)
			switch mode {
			case "input":
				a.CreationCommand.Inverse.DeleteInput.Title = "Unrequested edit"
			case "original":
				a.CreationCommand.Partial = true
			case "checkpoint":
				a.CreationCommand.Inverse.Checkpoint = nil
			case "proof":
				a.CreationCommand.Inverse.Result.Proof = "forged"
			case "author":
				a.CreationCommand.Inverse.Result.Actor = "other-actor"
			case "clock":
				a.Restored.UpdatedAt = a.After.UpdatedAt.Add(-time.Second)
				a.Restored.Revision, _ = a.Restored.checksum()
			case "creation-owner":
				_, child := creationRootFixture(t, true)
				changed := child.ActionJournal
				changed.CreationOwner = "other-owner"
				if err := validateGroupRewrite(child.ActionJournal, changed); err == nil {
					t.Fatal("child ownership changed")
				}
				return
			}
			if mode == "author" {
				if creationNativeAuthor(a, "github-author") {
					t.Fatal("foreign inverse attribution accepted")
				}
			} else if err := a.validate(); err == nil && validateCreationInverseRewrite(checkpoint, a) == nil {
				t.Fatal("changed inverse evidence accepted")
			}
		})
	}
}

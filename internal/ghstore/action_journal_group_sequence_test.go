package ghstore

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/marcus/td/internal/reviewpolicy"
)

// Synthetic actors and IDs here are fixture data, never review attestations.
func orderedClosureFixture(t *testing.T) (*ActionJournalRecord, []*ActionJournalRecord) {
	t.Helper()
	f, _, _ := workflowUndoFixture(t, "unblock", false)
	first, err := decodeActionJournal(*f.comment, "gh-9")
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []*ActionIssueState{first.Before, first.After} {
		desc, meta, _, err := decodeBody(state.Body)
		if err != nil {
			t.Fatal(err)
		}
		meta.Details.Dependencies = []string{"gh-2"}
		if state == first.After {
			meta.Details.Transitions[len(meta.Details.Transitions)-1].Reason = "Auto-unblocked (dependency gh-2 closed)"
			meta.LastStateReason = "Auto-unblocked (dependency gh-2 closed)"
		}
		state.Body, err = encodeBody(desc, meta)
		if err != nil {
			t.Fatal(err)
		}
		state.Revision, _ = state.checksum()
	}
	first.UpdateIntent.Target.Body = first.After.Body
	issue := f.issues[1]
	issue.Body = first.After.Body
	issue.UpdatedAt = first.After.UpdatedAt
	f.issues[1] = issue
	if _, _, err := f.client.Transition(context.Background(), "gh-1", "close", TransitionOptions{SessionID: "ses_actor", Mode: reviewpolicy.ModeTrusted, AdminReason: "Synthetic explicit close after unblock", Reason: "Synthetic explicit close"}); err != nil {
		t.Fatal(err)
	}
	second, err := decodeActionJournal(*f.comment, "gh-9")
	if err != nil {
		t.Fatal(err)
	}
	group := &ActionJournalRecord{ActionJournal: ActionJournal{OperationID: "td-op-ordered-group", SessionID: "ses_actor", IssueID: "gh-2", CarrierID: "gh-9", Action: "group", Phase: "prepared", Group: &ActionGroup{Command: "close", Roots: []string{"gh-2", "gh-1"}, Cascade: true, Members: []ActionGroupMember{{IssueID: "gh-2", Before: journalStateFixture(t, "Root")}, {IssueID: "gh-1", Before: *first.Before}}}}, ID: "ghc-200", Author: first.Author, CreatedAt: first.CreatedAt.Add(-time.Minute)}
	first.ID, second.ID = "ghc-201", "ghc-202"
	first.GroupOperationID, second.GroupOperationID = group.OperationID, group.OperationID
	return group, []*ActionJournalRecord{first, second}
}

func TestOrderedGroupEffectsValidateActualRepeatedTarget(t *testing.T) {
	group, effects := orderedClosureFixture(t)
	before, _ := json.Marshal(effects)
	chains, err := orderedGroupEffects(group, []*ActionJournalRecord{effects[1], effects[0]})
	if err != nil || len(chains["gh-1"]) != 2 || chains["gh-1"][0] != effects[0] || chains["gh-1"][1] != effects[1] {
		t.Fatal("auto-unblock/explicit close chain rejected", err)
	}
	after, _ := json.Marshal(effects)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("sequence validation rewrote original receipts")
	}
	if err := validateGroupMember(group, effects[1]); err == nil {
		t.Fatal("legacy member validator silently accepted an intermediate state")
	}
}

func TestOrderedGroupEffectsRejectBrokenAndUncertainEdges(t *testing.T) {
	for _, mode := range []string{"predecessor-clock", "predecessor-body", "missing-after", "uncertain-first", "duplicate-operation", "duplicate-receipt", "foreign-actor", "outside-membership", "clock-regression", "forged-result", "inverse-as-predecessor"} {
		t.Run(mode, func(t *testing.T) {
			group, effects := orderedClosureFixture(t)
			switch mode {
			case "predecessor-clock":
				effects[1].Before.UpdatedAt = effects[1].Before.UpdatedAt.Add(time.Second)
				effects[1].Before.Revision, _ = effects[1].Before.checksum()
			case "predecessor-body":
				effects[1].Before.Body += "foreign edit"
				effects[1].Before.Revision, _ = effects[1].Before.checksum()
			case "missing-after":
				effects[0].After = nil
			case "uncertain-first":
				effects[0].Phase, effects[0].After = "prepared", nil
			case "duplicate-operation":
				effects[1].OperationID = effects[0].OperationID
			case "duplicate-receipt":
				effects[1].ID = effects[0].ID
			case "foreign-actor":
				effects[1].Author = "foreign-actor"
			case "outside-membership":
				effects[1].IssueID = "gh-3"
			case "clock-regression":
				effects[1].CreatedAt = effects[0].CreatedAt.Add(-time.Second)
			case "forged-result":
				effects[0].After.Title = "foreign result"
				effects[0].After.Revision, _ = effects[0].After.checksum()
			case "inverse-as-predecessor":
				copy := *effects[0].Before
				effects[0].Restored, effects[0].Phase = &copy, "undone"
				effects[1].Before = &copy
			}
			if _, err := orderedGroupEffects(group, effects); err == nil {
				t.Fatal("invalid forward edge accepted")
			}
		})
	}
}

func TestOrderedGroupEffectsKeepUncertaintyAndNativeEventEdges(t *testing.T) {
	group, effects := orderedClosureFixture(t)
	effects[1].Phase, effects[1].After = "prepared", nil
	if _, err := orderedGroupEffects(group, effects); err != nil {
		t.Fatal("terminal uncertain input rejected", err)
	}
	if effects[1].Phase != "prepared" || effects[1].After != nil {
		t.Fatal("uncertain effect was promoted to a result")
	}
	group, effects = orderedClosureFixture(t)
	baseline := []nativeStateEvent{}
	effects[0].UpdateIntent.NativeEvents = &baseline
	if _, err := orderedGroupEffects(group, effects); err != nil {
		t.Fatal("unchanged event continuation rejected", err)
	}
	for _, mode := range []string{"missing", "extra-event"} {
		t.Run(mode, func(t *testing.T) {
			group, effects := orderedClosureFixture(t)
			baseline := []nativeStateEvent{}
			effects[0].UpdateIntent.NativeEvents = &baseline
			if mode == "missing" {
				effects[1].UpdateIntent.NativeEvents = nil
			} else {
				foreign := []nativeStateEvent{{ID: 999, Event: "reopened"}}
				effects[1].UpdateIntent.NativeEvents = &foreign
			}
			if _, err := orderedGroupEffects(group, effects); err == nil {
				t.Fatal("broken native event edge accepted")
			}
		})
	}
}

package ghstore

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/marcus/td/internal/models"
)

// All clocks, events and actors in these pure planner tests are synthetic.
func orderedCompensationFixture(t *testing.T) (*ActionJournalRecord, []*ActionJournalRecord, map[string][]nativeStateEvent, *OrderedGroupUndoStep) {
	t.Helper()
	group, effects := orderedClosureFixture(t)
	baseline := append([]nativeStateEvent{}, (*effects[1].UpdateIntent.NativeEvents)...)
	last := int64(0)
	if len(baseline) > 0 {
		last = baseline[len(baseline)-1].ID
	}
	baseline = append(baseline, nativeStateEvent{ID: last + 10, Event: "closed"})
	forward := map[string][]nativeStateEvent{"gh-1": baseline}
	step, err := PlanOrderedGroupUndoStep(group, effects, forward, nil, *effects[1].After, baseline, "td-op-group-inverse", "td-op-inverse-first", group.SessionID, group.Author, time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return group, effects, forward, step
}

func completeOrderedStepFixture(t *testing.T, step *OrderedGroupUndoStep, observed bool) {
	t.Helper()
	state, err := step.Input.contents()
	if err != nil {
		t.Fatal(err)
	}
	state.UpdatedAt = step.At.Add(time.Second)
	state.Revision, _ = state.checksum()
	events := append([]nativeStateEvent{}, step.Baseline...)
	if step.Source.State != step.Input.State {
		last := int64(0)
		if len(events) > 0 {
			last = events[len(events)-1].ID
		}
		kind := "closed"
		if step.Input.State == "open" {
			kind = "reopened"
		}
		events = append(events, nativeStateEvent{ID: last + 10, Event: kind})
	}
	step.Result, step.EventsAfter, step.Proof = &state, &events, "verified-group-inverse"
	if observed {
		step.Proof, step.ObserverSession, step.ObservedAt = inverseObservationProof, "ses_observer", state.UpdatedAt.Add(time.Second)
	}
}

func nextOrderedFixturePlan(t *testing.T, group *ActionJournalRecord, effects []*ActionJournalRecord, forward map[string][]nativeStateEvent, first *OrderedGroupUndoStep) (*OrderedGroupUndoStep, error) {
	t.Helper()
	return PlanOrderedGroupUndoStep(group, effects, forward, []*OrderedGroupUndoStep{first}, *first.Result, *first.EventsAfter, first.GroupUndoOperationID, "td-op-inverse-second", group.SessionID, group.Author, first.Result.UpdatedAt.Add(time.Minute))
}

func TestOrderedGroupCompensationPreservesNativeAndScalarAudit(t *testing.T) {
	group, effects, forward, first := orderedCompensationFixture(t)
	original, _ := json.Marshal(effects)
	if first.Input.State != "open" || first.Result != nil || first.Proof != "" || first.EventsAfter != nil {
		t.Fatal("planned native inverse asserted a completion", first)
	}
	completeOrderedStepFixture(t, first, false)
	firstBefore, _ := json.Marshal(first)
	second, err := nextOrderedFixturePlan(t, group, effects, forward, first)
	if err != nil {
		t.Fatal("native compensation could not precede scalar compensation", err)
	}
	desc, bm, _, _ := decodeBody(effects[0].Before.Body)
	newDesc, final, _, err := decodeBody(second.Input.Body)
	if err != nil || desc != newDesc || final.Details.Status != models.StatusBlocked || second.Input.State != "open" || second.Input.Title != effects[0].Before.Title || !reflect.DeepEqual(second.Input.Labels, effects[0].Before.Labels) {
		t.Fatal("former task fields were not compensated", err)
	}
	_, after, _, _ := decodeBody(effects[1].After.Body)
	d := final.Details
	if len(d.Transitions) != len(after.Details.Transitions)+2 || !reflect.DeepEqual(d.Transitions[:len(after.Details.Transitions)], after.Details.Transitions) || !reflect.DeepEqual(d.Sessions, after.Details.Sessions) || d.Transitions[len(d.Transitions)-2].OperationID != first.OperationID || d.Transitions[len(d.Transitions)-1].OperationID != second.OperationID {
		t.Fatal("forward or preceding inverse audit was erased")
	}
	if !reflect.DeepEqual(d.Dependencies, bm.Details.Dependencies) || d.ImplementerSession != bm.Details.ImplementerSession || d.ReviewBasis != "" || d.ReviewerSession != "" || d.ReviewedAt != nil || d.ReviewHandoffID != "" {
		t.Fatal("former fields or invalidated review changed")
	}
	completeOrderedStepFixture(t, second, false)
	if !reflect.DeepEqual(first.EventsAfter, second.EventsAfter) || len(*second.EventsAfter) != len(forward["gh-1"])+1 {
		t.Fatal("scalar compensation invented a native event")
	}
	if err := orderedStepEventProof(second, *second.EventsAfter, true); err != nil {
		t.Fatal(err)
	}
	copyEffects, _ := json.Marshal(effects)
	copyFirst, _ := json.Marshal(first)
	if !reflect.DeepEqual(original, copyEffects) || !reflect.DeepEqual(firstBefore, copyFirst) {
		t.Fatal("planner modified original receipts or completed compensation")
	}
}

func TestOrderedGroupCompensationKeepsObservedProvenance(t *testing.T) {
	group, effects, forward, first := orderedCompensationFixture(t)
	completeOrderedStepFixture(t, first, true)
	before, _ := json.Marshal(first)
	second, err := nextOrderedFixturePlan(t, group, effects, forward, first)
	if err != nil || second.Proof != "" || second.Result != nil {
		t.Fatal("observed group continuation failed or invented a result", err)
	}
	after, _ := json.Marshal(first)
	if !reflect.DeepEqual(before, after) || first.Proof != inverseObservationProof || first.ObserverSession != "ses_observer" {
		t.Fatal("observed predecessor was promoted to verified proof")
	}
}

func TestOrderedGroupCompensationRejectsForeignOrPendingBridge(t *testing.T) {
	for _, mode := range []string{"pending", "wrong-receipt", "wrong-group", "wrong-inverse", "foreign-actor", "input", "result", "result-clock", "source", "source-clock", "event-prefix", "events-after", "promoted-proof", "reused-operation"} {
		t.Run(mode, func(t *testing.T) {
			group, effects, forward, first := orderedCompensationFixture(t)
			completeOrderedStepFixture(t, first, false)
			switch mode {
			case "pending":
				first.Proof = ""
			case "wrong-receipt":
				first.ForwardReceiptID = effects[0].ID
			case "wrong-group":
				first.GroupOperationID = "td-op-foreign-group"
			case "wrong-inverse":
				first.GroupUndoOperationID = "td-op-foreign-inverse"
				group.UndoOperationID = "td-op-group-inverse"
			case "foreign-actor":
				first.Actor = "foreign-actor"
			case "input":
				first.Input.Body += "foreign edit"
			case "result":
				first.Result.Title = "foreign result"
				first.Result.Revision, _ = first.Result.checksum()
			case "result-clock":
				first.Result.UpdatedAt = first.Source.UpdatedAt.Add(-time.Second)
				first.Result.Revision, _ = first.Result.checksum()
			case "source":
				first.Source.Body += "foreign source"
				first.Source.Revision, _ = first.Source.checksum()
			case "source-clock":
				first.Source.UpdatedAt = first.Source.UpdatedAt.Add(time.Second)
				first.Source.Revision, _ = first.Source.checksum()
			case "event-prefix":
				first.Baseline[0].ID++
			case "events-after":
				(*first.EventsAfter)[len(*first.EventsAfter)-1].ID++
				(*first.EventsAfter)[len(*first.EventsAfter)-1].Event = "closed"
			case "promoted-proof":
				first.ObserverSession = "ses_observer"
				first.ObservedAt = first.At.Add(time.Second)
			case "reused-operation":
				first.OperationID = "td-op-inverse-second"
			}
			if _, err := nextOrderedFixturePlan(t, group, effects, forward, first); err == nil {
				t.Fatal("unverified or foreign compensation bridge accepted")
			}
		})
	}
}

func TestOrderedGroupCompensationInvalidatesHistoricalApproval(t *testing.T) {
	for _, mode := range []string{"valid", "forged-review-events", "reused-group-identity"} {
		t.Run(mode, func(t *testing.T) {
			effect, _ := nativeUndoPlanFixture(t, "close-approved")
			group := &ActionJournalRecord{ActionJournal: ActionJournal{OperationID: "td-op-approved-group", SessionID: effect.SessionID, IssueID: effect.IssueID, CarrierID: effect.CarrierID, Action: "group", Phase: "prepared", Group: &ActionGroup{Command: "close", Roots: []string{effect.IssueID}, Cascade: true, Members: []ActionGroupMember{{IssueID: effect.IssueID, Before: *effect.Before}, {IssueID: "gh-2", Before: journalStateFixture(t, "Watched")}}}}, ID: "ghc-1", Author: effect.Author}
			effect.GroupOperationID = group.OperationID
			if mode == "forged-review-events" {
				for _, state := range []*ActionIssueState{effect.Before, effect.After} {
					desc, meta, _, _ := decodeBody(state.Body)
					meta.Details.ReviewEvents = "forged-event-digest"
					state.Body, _ = encodeBody(desc, meta)
					state.Revision, _ = state.checksum()
				}
				group.Group.Members[0].Before = *effect.Before
				effect.UpdateIntent.Target.Body = effect.After.Body
			}
			baseline := append([]nativeStateEvent{}, (*effect.UpdateIntent.NativeEvents)...)
			last := int64(0)
			if len(baseline) > 0 {
				last = baseline[len(baseline)-1].ID
			}
			baseline = append(baseline, nativeStateEvent{ID: last + 10, Event: "closed"})
			groupUndo := "td-op-approved-inverse"
			if mode == "reused-group-identity" {
				groupUndo = effect.OperationID
			}
			step, err := PlanOrderedGroupUndoStep(group, []*ActionJournalRecord{effect}, map[string][]nativeStateEvent{effect.IssueID: baseline}, nil, *effect.After, baseline, groupUndo, "td-op-approved-step", effect.SessionID, effect.Author, time.Now().UTC().Add(time.Hour))
			if mode != "valid" {
				if err == nil {
					t.Fatal("invalid historical proof admitted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			_, current, _, _ := decodeBody(effect.After.Body)
			_, restored, _, _ := decodeBody(step.Input.Body)
			if !step.RequiresNewReview || len(restored.Details.Reviews) != len(current.Details.Reviews) || restored.Details.ReviewerSession != "" || restored.Details.ReviewedAt != nil || restored.Details.ReviewBasis != "" || restored.Details.ReviewEvents != "" || restored.Details.ReviewHandoffID != "" {
				t.Fatal("historical approval revived or lost")
			}
			for _, row := range restored.Details.Reviews {
				if row.SupersededAt == nil || !row.SupersededAt.Equal(step.At) {
					t.Fatal("approval supersession omitted")
				}
			}
		})
	}
}

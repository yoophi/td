package ghstore

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"
)

const actionNativeUndoJournalStart = actionJournalPrefix + "v7\n"

// Original state and audit remain immutable. Native compensation records its
// planned payload and the event baseline separately from verified/observed proof.
type ActionNativeUndo struct {
	Plan            NativeIssueUndoPlan `json:"plan"`
	Baseline        []nativeStateEvent  `json:"baseline"`
	Proof           string              `json:"proof,omitempty"`
	EventsAfter     []nativeStateEvent  `json:"events_after,omitempty"`
	ObserverSession string              `json:"observer_session,omitempty"`
	ObservedAt      time.Time           `json:"observed_at,omitempty"`
}

func nativePlanContents(plan NativeIssueUndoPlan) (ActionIssueState, error) {
	data, err := json.Marshal(plan.Payload)
	if err != nil {
		return ActionIssueState{}, err
	}
	var state ActionIssueState
	if err := json.Unmarshal(data, &state); err != nil {
		return state, err
	}
	state.Revision, err = state.checksum()
	return state, err
}

func actionStateRecord(state *ActionIssueState, id string) (*Record, error) {
	n, err := Number(id)
	if err != nil {
		return nil, err
	}
	item := apiIssue{Number: n, Title: state.Title, Body: state.Body, State: state.State, UpdatedAt: state.UpdatedAt}
	for _, label := range state.Labels {
		item.Labels = append(item.Labels, struct {
			Name string `json:"name"`
		}{label})
	}
	data, err := json.Marshal(item)
	if err != nil {
		return nil, err
	}
	return decodeIssue(data)
}

func sameNativePlan(a, b NativeIssueUndoPlan) bool {
	left, err := json.Marshal(a)
	if err != nil {
		return false
	}
	right, err := json.Marshal(b)
	return err == nil && string(left) == string(right)
}

func nativeUndoEventProof(n *ActionNativeUndo, events []nativeStateEvent, after bool) error {
	if len(n.Baseline) == 0 {
		return fmt.Errorf("native undo requires its observed forward state event")
	}
	last := int64(0)
	for _, event := range n.Baseline {
		if event.ID <= last || (event.Event != "closed" && event.Event != "reopened") {
			return fmt.Errorf("native undo event baseline is invalid")
		}
		last = event.ID
	}
	if n.Baseline[len(n.Baseline)-1].Event != n.Plan.ForwardNativeEvent {
		return fmt.Errorf("native undo forward event differs from the planned writer")
	}
	if !after {
		if !reflect.DeepEqual(events, n.Baseline) {
			return fmt.Errorf("native state events changed before compensation")
		}
		return nil
	}
	if len(events) != len(n.Baseline)+1 || !reflect.DeepEqual(events[:len(n.Baseline)], n.Baseline) || events[len(n.Baseline)].ID <= last || events[len(n.Baseline)].Event != n.Plan.InverseNativeEvent {
		return fmt.Errorf("native inverse event is missing or state history changed; inspect before retrying, no target replay")
	}
	return nil
}

// Validate the historical review against the actual event prefix before the
// forward close/reopen, never rebase it onto the compensated event history.
func nativeUndoHistoricalReview(a *ActionJournalRecord, n *ActionNativeUndo) error {
	before, err := actionStateRecord(a.Before, a.IssueID)
	if err != nil {
		return err
	}
	d, err := before.CopyDetails()
	if err != nil {
		return err
	}
	if d.ReviewBasis == "" {
		return nil
	}
	if d.ReviewBasis != reviewBasis(before, d) {
		return fmt.Errorf("native undo original review basis is stale")
	}
	if len(n.Baseline) == 0 {
		return fmt.Errorf("native undo review has no observed event baseline")
	}
	prefix := n.Baseline[:len(n.Baseline)-1]
	if a.Before.State == "open" {
		if stateEventDigest(prefix) != d.ReviewEvents {
			return fmt.Errorf("native undo original review events changed")
		}
		return nil
	}
	for i, event := range prefix {
		if event.Event == "closed" {
			old := make([]nativeStateEvent, 0, len(prefix)-1)
			old = append(old, prefix[:i]...)
			old = append(old, prefix[i+1:]...)
			if stateEventDigest(old) == d.ReviewEvents {
				return nil
			}
		}
	}
	return fmt.Errorf("native undo original closed approval events cannot be verified")
}

func (a ActionJournal) validateNativeUndo() error {
	n := a.NativeUndo
	if n == nil {
		return nil
	}
	if (a.Phase != "undo_prepared" && a.Phase != "undone") || a.Before == nil || a.After == nil || a.Reconciliation != nil || a.UndoObservation != nil || n.Plan.Actor == "" || n.Plan.SessionID != a.SessionID || n.Plan.UndoOperationID != a.UndoOperationID {
		return fmt.Errorf("native compensation requires an attributed undo intent")
	}
	original := a
	original.NativeUndo = nil
	original.Phase = "applied"
	original.UndoOperationID = ""
	original.Restored = nil
	record := ActionJournalRecord{ActionJournal: original, Author: n.Plan.Actor}
	current, err := actionStateRecord(a.After, a.IssueID)
	if err != nil {
		return err
	}
	expected, err := record.PlanNativeIssueUndo(current, a.SessionID, n.Plan.Actor, a.UndoOperationID, n.Plan.At)
	if err != nil {
		return err
	}
	if !sameNativePlan(*expected, n.Plan) {
		return fmt.Errorf("native compensation cannot replace its original policy or planned inverse")
	}
	if err := nativeUndoEventProof(n, n.Baseline, false); err != nil {
		return err
	}
	if err := nativeUndoHistoricalReview(&record, n); err != nil {
		return err
	}
	if a.Phase == "undo_prepared" {
		if n.Proof != "" || len(n.EventsAfter) != 0 || n.ObserverSession != "" || !n.ObservedAt.IsZero() {
			return fmt.Errorf("native intent cannot assert a completed inverse")
		}
		return nil
	}
	contents, err := nativePlanContents(n.Plan)
	if err != nil {
		return err
	}
	if a.Restored == nil || !sameActionContents(contents, *a.Restored) || a.Restored.UpdatedAt.Before(a.After.UpdatedAt) {
		return fmt.Errorf("native compensation restored state differs from its plan")
	}
	if err := nativeUndoEventProof(n, n.EventsAfter, true); err != nil {
		return err
	}
	switch n.Proof {
	case "verified-native-inverse":
		if n.ObserverSession != "" || !n.ObservedAt.IsZero() {
			return fmt.Errorf("verified native inverse cannot rewrite observer provenance")
		}
	case inverseObservationProof:
		probe := ActionJournal{OperationID: "probe", SessionID: n.ObserverSession, IssueID: "gh-1", Action: "create", Phase: "prepared"}
		if err := probe.validate(); err != nil || n.ObservedAt.IsZero() || n.ObservedAt.Before(n.Plan.At) {
			return fmt.Errorf("native observation needs separate valid provenance")
		}
	default:
		return fmt.Errorf("native compensation requires explicit inverse proof")
	}
	return nil
}

func validateNativeUndoRewrite(old, next *ActionNativeUndo) error {
	if old == nil {
		return nil
	}
	if next == nil || !sameNativePlan(old.Plan, next.Plan) || !reflect.DeepEqual(old.Baseline, next.Baseline) || old.Proof != "" {
		return fmt.Errorf("native undo cannot replace its immutable intent")
	}
	return nil
}

func nativeUndoOutcome(n *ActionNativeUndo) *ActionUndoOutcome {
	return &ActionUndoOutcome{Proof: n.Proof, TargetWriteVerified: strings.EqualFold(n.Proof, "verified-native-inverse"), CausalUndoChainEligible: false}
}

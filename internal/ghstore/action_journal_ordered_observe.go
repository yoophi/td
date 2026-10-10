package ghstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
)

type orderedGroupObservation struct {
	Members        []ActionGroupResultMember
	Records        map[string]Record
	Effects        []OrderedGroupEffect
	Events         map[string][]nativeStateEvent
	PendingReceipt string
	PendingStep    bool
	PendingApplied bool
	Remaining      bool
	Partial        bool
}

func orderedEffectSnapshot(a *ActionJournalRecord, status string) (OrderedGroupEffect, error) {
	body, err := renderExistingActionJournal(a)
	if err != nil {
		return OrderedGroupEffect{}, err
	}
	digest := sha256.Sum256([]byte(body))
	return OrderedGroupEffect{Record: a, BodySHA256: hex.EncodeToString(digest[:]), Status: status}, nil
}

func sameOrderedEffect(a, b OrderedGroupEffect) bool {
	left, err := json.Marshal(a)
	if err != nil {
		return false
	}
	right, err := json.Marshal(b)
	return err == nil && string(left) == string(right)
}

// A point read binds the exact native body, including compressed bytes, and
// every public identity/result field from the frozen original receipt.
func (c *Client) pointOrderedEffect(ctx context.Context, expected OrderedGroupEffect) error {
	if expected.Record == nil {
		return fmt.Errorf("ordered original receipt is missing")
	}
	n, err := CommentNumber(expected.Record.ID)
	if err != nil {
		return err
	}
	data, err := c.request(withReviewCost(ctx), "GET", fmt.Sprintf("/issues/comments/%d", n), nil, false)
	if err != nil {
		return err
	}
	var raw apiComment
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	actual, err := decodeActionJournal(raw, expected.Record.CarrierID)
	if err != nil {
		return fmt.Errorf("ordered original receipt is missing or invalid: %w", err)
	}
	if actual == nil {
		return fmt.Errorf("ordered original receipt is missing or invalid")
	}
	observed, err := orderedEffectSnapshot(actual, expected.Status)
	if err != nil {
		return err
	}
	if !sameOrderedEffect(expected, observed) {
		return fmt.Errorf("ordered original receipt identity, clock, body or result changed")
	}
	return nil
}

// One fresh bulk history supplies membership, revisions and all original
// attempts. Event endpoints are read only for affected targets. No record or
// target is changed by observation, including an uncertain inverse.
func (c *Client) observeOrderedGroup(ctx context.Context, a *ActionJournalRecord, normal bool) (*orderedGroupObservation, error) {
	snapshot, err := c.ReadSnapshot(WithFreshSnapshot(ctx), true)
	if err != nil {
		return nil, err
	}
	if err := verifyGraphGroupMembership(a.Group, snapshot.Issues(true)); err != nil {
		return nil, err
	}
	history, err := snapshot.ActionHistory("")
	if err != nil {
		return nil, err
	}
	out := &orderedGroupObservation{Records: map[string]Record{}, Events: map[string][]nativeStateEvent{}}
	carriers, roots := 0, 0
	for _, record := range snapshot.records {
		if record.meta.EntityKind == "journal" {
			carriers++
			if record.ID != a.CarrierID {
				return nil, fmt.Errorf("ordered group carrier is ambiguous")
			}
		}
		out.Records[record.ID] = record
	}
	attempts := []*ActionJournalRecord{}
	for i := range history {
		r := &history[i]
		if r.ID == a.ID {
			roots++
			if !reflect.DeepEqual(*r, *a) {
				return nil, fmt.Errorf("ordered group receipt changed during observation")
			}
		}
		if r.GroupOperationID == a.OperationID {
			attempts = append(attempts, r)
		}
	}
	if carriers != 1 || roots != 1 {
		return nil, fmt.Errorf("ordered observation needs its exact group and one carrier")
	}
	chains, err := orderedGroupEffects(a, attempts)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(attempts, func(x, y *ActionJournalRecord) int { return x.GroupSequence - y.GroupSequence })
	states := map[string]ActionIssueState{}
	for _, m := range a.Group.Members {
		r, found := out.Records[m.IssueID]
		if !found {
			return nil, &ConflictError{ID: m.IssueID}
		}
		state, err := actionIssueState(&r)
		if err != nil {
			return nil, err
		}
		states[m.IssueID] = state
	}
	eventTargets := map[string]bool{}
	for id := range chains {
		eventTargets[id] = true
	}
	for id := range a.Group.Ordered.Events {
		eventTargets[id] = true
	}
	for id := range eventTargets {
		events, err := c.readStateEvents(withReviewCost(ctx), id)
		if err != nil {
			return nil, err
		}
		out.Events[id] = events
	}
	for _, r := range attempts {
		status := "applied"
		if r.Phase == "prepared" {
			if a.Phase != "prepared" {
				status = "unapplied"
			} else if states[r.IssueID].Revision == r.Before.Revision {
				status, out.Partial = "unapplied", true
				if r.UpdateIntent.NativeEvents != nil && !reflect.DeepEqual(*r.UpdateIntent.NativeEvents, out.Events[r.IssueID]) {
					return nil, &ConflictError{ID: r.IssueID}
				}
			} else {
				want, err := r.UpdateIntent.Target.contents()
				if err != nil || !sameActionContents(want, states[r.IssueID]) || states[r.IssueID].UpdatedAt.Before(r.Before.UpdatedAt) {
					return nil, &ConflictError{ID: r.IssueID}
				}
				if r.UpdateIntent.NativeEvents != nil {
					if err := updateEventProof(r.ActionJournal, out.Events[r.IssueID]); err != nil {
						return nil, err
					}
				}
				status, out.PendingReceipt = "pending-applied", r.ID
			}
		} else if r.Phase != "applied" {
			return nil, fmt.Errorf("ordered original member acquired an individual inverse")
		} else if r.UpdateObservation != nil {
			status = "observed"
		}
		effect, err := orderedEffectSnapshot(r, status)
		if err != nil {
			return nil, err
		}
		out.Effects = append(out.Effects, effect)
	}
	if normal && (out.PendingReceipt != "" || out.Partial) {
		return nil, fmt.Errorf("successful ordered callback contains an unverified forward attempt")
	}
	if a.Phase != "prepared" {
		if len(out.Effects) != len(a.Group.Ordered.Forward) {
			return nil, fmt.Errorf("ordered group original attempts changed after freezing")
		}
		for i, effect := range out.Effects {
			if !sameOrderedEffect(effect, a.Group.Ordered.Forward[i]) {
				return nil, fmt.Errorf("ordered group original receipt changed after freezing")
			}
		}
		return c.observeOrderedInverse(ctx, a, out, states)
	}
	for _, m := range a.Group.Members {
		state := states[m.IssueID]
		result := ActionGroupResultMember{IssueID: m.IssueID, State: state}
		chain := chains[m.IssueID]
		if len(chain) == 0 {
			if state.Revision != m.Before.Revision {
				return nil, &ConflictError{ID: m.IssueID}
			}
			result.Status = "unattempted"
			if normal {
				result.Status = "noop"
			} else {
				out.Partial = true
			}
		} else {
			last := chain[len(chain)-1]
			result.ReceiptID, result.Status = last.ID, out.Effects[last.GroupSequence-1].Status
			want := last.After
			if result.Status == "unapplied" {
				want = last.Before
				if len(chain) > 1 {
					last = chain[len(chain)-2]
					result.ReceiptID, result.Status = last.ID, out.Effects[last.GroupSequence-1].Status
				}
			}
			if result.Status != "pending-applied" && (want == nil || state.Revision != want.Revision) {
				return nil, &ConflictError{ID: m.IssueID}
			}
			if last.Phase == "applied" && last.UpdateIntent.NativeEvents != nil {
				if err := updateEventProof(last.ActionJournal, out.Events[m.IssueID]); err != nil {
					return nil, err
				}
			}
		}
		out.Members = append(out.Members, result)
	}
	return out, nil
}

func (c *Client) observeOrderedInverse(_ context.Context, a *ActionJournalRecord, out *orderedGroupObservation, states map[string]ActionIssueState) (*orderedGroupObservation, error) {
	expected, expectedEvents := map[string]ActionIssueState{}, map[string][]nativeStateEvent{}
	for _, member := range a.Group.Forward.Members {
		expected[member.IssueID] = member.State
	}
	for id, events := range a.Group.Ordered.Events {
		expectedEvents[id] = events
	}
	byReceipt, totals, completed := map[string]string{}, map[string]int{}, map[string]int{}
	for _, effect := range out.Effects {
		byReceipt[effect.Record.ID] = effect.Record.IssueID
		if effect.Status != "unapplied" {
			totals[effect.Record.IssueID]++
		}
	}
	for _, step := range a.Group.Ordered.Steps {
		id := byReceipt[step.ForwardReceiptID]
		if step.Result == nil {
			out.PendingStep = true
			state := states[id]
			want, err := step.Input.contents()
			if err != nil {
				return nil, err
			}
			if sameActionContents(want, state) && !state.UpdatedAt.Before(step.Source.UpdatedAt) {
				if err := orderedStepEventProof(step, out.Events[id], true); err != nil {
					return nil, err
				}
				out.PendingApplied = true
				expected[id], expectedEvents[id] = state, out.Events[id]
			} else if state.Revision != step.Source.Revision || !reflect.DeepEqual(out.Events[id], step.Baseline) {
				return nil, &ConflictError{ID: id}
			}
		} else {
			expected[id], expectedEvents[id] = *step.Result, *step.EventsAfter
			completed[id]++
		}
	}
	for i, original := range a.Group.Forward.Members {
		id := original.IssueID
		state := states[id]
		if state.Revision != expected[id].Revision {
			return nil, &ConflictError{ID: id}
		}
		result := original
		result.State = state
		if completed[id] == totals[id] && totals[id] > 0 {
			result.Status = "undone"
		} else if completed[id] > 0 {
			result.Status = "partially-undone"
		}
		if completed[id] < totals[id] {
			out.Remaining = true
		}
		if a.Phase == "undone" && !reflect.DeepEqual(result, a.Group.Inverse.Members[i]) {
			return nil, fmt.Errorf("ordered inverse completion summary changed")
		}
		out.Members = append(out.Members, result)
	}
	for id, events := range expectedEvents {
		if !reflect.DeepEqual(events, out.Events[id]) {
			return nil, fmt.Errorf("ordered group native event observation changed")
		}
	}
	out.Partial = a.Group.Forward.Partial
	return out, nil
}

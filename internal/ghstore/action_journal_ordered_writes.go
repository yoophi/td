package ghstore

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"time"
)

func checkOrderedInitialCapacity(a ActionJournal, expected map[string]ActionIssueState) error {
	if err := checkJournalSizingProbe(a); err != nil {
		return err
	}
	probe := a
	g := *a.Group
	probe.Group, probe.Phase = &g, "applied"
	g.Ordered = &ActionOrderedGroup{Events: map[string][]nativeStateEvent{}}
	g.Forward = &ActionGroupObservation{SessionID: a.SessionID, Actor: strings.Repeat("x", 39), ObservedAt: time.Now().UTC(), Proof: "verified-group-receipts"}
	for _, member := range g.Members {
		g.Forward.Members = append(g.Forward.Members, ActionGroupResultMember{IssueID: member.IssueID, Status: "noop", State: expected[member.IssueID]})
	}
	record := &ActionJournalRecord{ActionJournal: probe, ID: "ghc-1", Author: g.Forward.Actor}
	observed := &orderedGroupObservation{Members: g.Forward.Members, Events: g.Ordered.Events}
	return checkOrderedInverseCapacity(record, observed, "td-op-"+strings.Repeat("x", 32))
}

func (c *Client) attachOrderedGroupMember(ctx context.Context, a *ActionJournal) error {
	active := c.journal.Group
	if n := len(active.Receipts); n > 0 && active.Receipts[n-1].Phase != "applied" {
		return fmt.Errorf("ordered group has an uncertain earlier attempt; reconcile before continuing, no target write attempted")
	}
	before, found := active.Expected[a.IssueID]
	if !found {
		return fmt.Errorf("ordered member is outside frozen membership")
	}
	a.GroupOperationID, a.GroupSequence = active.Journal.OperationID, len(active.Receipts)+1
	for i := len(active.Receipts) - 1; i >= 0; i-- {
		if active.Receipts[i].IssueID == a.IssueID {
			a.GroupPreviousReceiptID = active.Receipts[i].ID
			break
		}
	}
	var events []nativeStateEvent
	if a.UpdateIntent.NativeEvents != nil {
		events = slices.Clone(*a.UpdateIntent.NativeEvents)
	} else {
		var err error
		events, err = c.readStateEvents(withReviewCost(ctx), a.IssueID)
		if err != nil {
			return err
		}
		a.UpdateIntent.NativeEvents = &events
	}
	if previous, known := active.NativeEvents[a.IssueID]; known && !reflect.DeepEqual(previous, events) {
		return fmt.Errorf("ordered original event continuation changed; no target write attempted")
	}
	active.NativeEvents[a.IssueID] = events
	member := &ActionJournalRecord{ActionJournal: *a, Author: active.Journal.Author}
	if err := validateGroupMemberAtState(active.Journal, member, &before); err != nil {
		return err
	}
	// Reserve both possible actual outcomes of this attempt before posting its
	// intent. A later rejected input leaves earlier effects recoverable; no
	// unattempted future command input is represented as an observed result.
	for _, applied := range []bool{false, true} {
		probe, observed, err := orderedForwardSizingProbe(active, a, applied)
		if err != nil {
			return err
		}
		if err := checkOrderedInverseCapacity(probe, observed, "td-op-"+strings.Repeat("x", 32)); err != nil {
			return fmt.Errorf("ordered completion capacity preflight (no target or member intent write attempted): %w", err)
		}
	}
	return c.verifyGraphGroupExpected(ctx, active.Journal.Group, active.Expected)
}

// Sizing copies use actual prior receipts and the current complete requested
// input. Projected clocks, IDs, events and outcomes are private and never stored.
func orderedForwardSizingProbe(active *activeActionGroup, input *ActionJournal, applied bool) (*ActionJournalRecord, *orderedGroupObservation, error) {
	clock := time.Now().UTC()
	if !clock.After(active.Journal.CreatedAt) {
		clock = active.Journal.CreatedAt.Add(time.Nanosecond)
	}
	for _, r := range active.Receipts {
		if !clock.After(r.CreatedAt) {
			clock = r.CreatedAt.Add(time.Nanosecond)
		}
	}
	current := &ActionJournalRecord{ActionJournal: *input, ID: "ghc-9223372036854775807", Author: active.Journal.Author, CreatedAt: clock, UpdatedAt: clock}
	status := "unapplied"
	events := map[string][]nativeStateEvent{}
	for id, value := range active.NativeEvents {
		events[id] = slices.Clone(value)
	}
	if applied {
		state, err := input.UpdateIntent.Target.contents()
		if err != nil {
			return nil, nil, err
		}
		state.UpdatedAt = input.Before.UpdatedAt
		state.Revision, err = state.checksum()
		if err != nil {
			return nil, nil, err
		}
		current.Phase, current.After, status = "applied", &state, "applied"
		if input.Before.State != state.State {
			baseline := events[input.IssueID]
			last := int64(0)
			if len(baseline) > 0 {
				last = baseline[len(baseline)-1].ID
			}
			if last == math.MaxInt64 {
				return nil, nil, fmt.Errorf("ordered native event identity cannot advance")
			}
			kind := "closed"
			if state.State == "open" {
				kind = "reopened"
			}
			events[input.IssueID] = append(baseline, nativeStateEvent{ID: last + 1, Event: kind})
		}
	}
	next := copyOrderedJournal(active.Journal)
	next.Phase = "applied"
	next.Group.Ordered = &ActionOrderedGroup{Events: events}
	for _, record := range active.Receipts {
		effect, err := orderedEffectSnapshot(record, "applied")
		if err != nil {
			return nil, nil, err
		}
		next.Group.Ordered.Forward = append(next.Group.Ordered.Forward, effect)
	}
	effect, err := orderedEffectSnapshot(current, status)
	if err != nil {
		return nil, nil, err
	}
	next.Group.Ordered.Forward = append(next.Group.Ordered.Forward, effect)
	observation := &ActionGroupObservation{SessionID: next.SessionID, Actor: active.Journal.Author, ObservedAt: clock, Proof: "verified-group-receipts", Partial: !applied}
	for _, original := range next.Group.Members {
		result := ActionGroupResultMember{IssueID: original.IssueID, State: active.Expected[original.IssueID], Status: "noop"}
		if !applied {
			result.Status = "unattempted"
		}
		for _, f := range next.Group.Ordered.Forward {
			if f.Record.IssueID != original.IssueID {
				continue
			}
			if f.Status == "unapplied" && result.ReceiptID != "" {
				continue
			}
			result.ReceiptID, result.Status = f.Record.ID, f.Status
			if f.Status == "unapplied" {
				result.State = *f.Record.Before
			} else {
				result.State = *f.Record.After
			}
		}
		observation.Members = append(observation.Members, result)
	}
	next.Group.Forward = observation
	probe := *active.Journal
	probe.ActionJournal = next
	return &probe, &orderedGroupObservation{Members: observation.Members, Effects: next.Group.Ordered.Forward, Events: events, Partial: observation.Partial}, nil
}

// Fence all actual prior receipts plus this original input, not just the issue
// currently being PATCHed. After a target write, accept only its exact response
// state and the one required event; all other watched states stay unchanged.
func (c *Client) verifyOrderedForwardProgress(ctx context.Context, prepared *ActionJournalRecord, after *Record) ([]nativeStateEvent, error) {
	active := c.journal.Group
	observed, err := c.observeOrderedGroup(ctx, active.Journal, false)
	if err != nil {
		return nil, err
	}
	if len(observed.Effects) != len(active.Receipts) || len(active.Receipts) == 0 || active.Receipts[len(active.Receipts)-1].ID != prepared.ID {
		return nil, fmt.Errorf("ordered actual receipt membership changed")
	}
	for i, effect := range observed.Effects {
		if !reflect.DeepEqual(effect.Record, active.Receipts[i]) {
			return nil, fmt.Errorf("ordered original receipt changed")
		}
		original, err := actionStateRecord(effect.Record.Before, effect.Record.IssueID)
		if err != nil {
			return nil, err
		}
		details, err := original.CopyDetails()
		if err != nil {
			return nil, err
		}
		if details.ReviewBasis != "" {
			if err := c.verifyReviewHandoffObserved(withReviewCost(ctx), original, details, false); err != nil {
				return nil, err
			}
		}
	}
	want := active.Expected[prepared.IssueID]
	if after == nil {
		if observed.PendingReceipt != "" {
			return nil, fmt.Errorf("ordered target already changed before PATCH; inspect without replay")
		}
	} else {
		want, err = actionIssueState(after)
		if err != nil {
			return nil, err
		}
		if observed.PendingReceipt != prepared.ID {
			return nil, fmt.Errorf("ordered target result is not its pending original input")
		}
	}
	for _, member := range observed.Members {
		expected := active.Expected[member.IssueID]
		if member.IssueID == prepared.IssueID {
			expected = want
		}
		if member.State.Revision != expected.Revision {
			return nil, &ConflictError{ID: member.IssueID, AfterWrite: after != nil}
		}
	}
	for id, previous := range active.NativeEvents {
		actual := observed.Events[id]
		if id == prepared.IssueID && after != nil {
			if err := updateEventProof(prepared.ActionJournal, actual); err != nil {
				return nil, err
			}
		} else if !reflect.DeepEqual(actual, previous) {
			return nil, fmt.Errorf("ordered watched native events changed")
		}
	}
	if err := c.fenceOrderedObservation(ctx, active.Journal, observed, false); err != nil {
		return nil, err
	}
	return slices.Clone(observed.Events[prepared.IssueID]), nil
}

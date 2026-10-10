package ghstore

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"
)

const forwardObservationProof = "forward-contents-observed"

// A saved intent proves what was requested, not who performed an uncertain
// target write. Keep actual observer attribution separate from that intent.
type ActionUpdateObservation struct {
	SessionID    string              `json:"session_id"`
	Actor        string              `json:"actor"`
	ObservedAt   time.Time           `json:"observed_at"`
	Proof        string              `json:"proof"`
	State        *ActionIssueState   `json:"state"`
	NativeEvents *[]nativeStateEvent `json:"native_events,omitempty"`
}

func updateEventProof(a ActionJournal, events []nativeStateEvent) error {
	if a.UpdateIntent == nil || a.UpdateIntent.NativeEvents == nil {
		if events != nil {
			return fmt.Errorf("update observation cannot invent an event baseline")
		}
		return nil
	}
	before := *a.UpdateIntent.NativeEvents
	if a.Before.State == a.UpdateIntent.Target.State {
		if !reflect.DeepEqual(before, events) {
			return fmt.Errorf("update native events changed after its recorded intent")
		}
		return nil
	}
	kind := "closed"
	if a.UpdateIntent.Target.State == "open" {
		kind = "reopened"
	}
	last := int64(0)
	if len(before) > 0 {
		last = before[len(before)-1].ID
	}
	if len(events) != len(before)+1 || !reflect.DeepEqual(events[:len(before)], before) || events[len(before)].ID <= last || events[len(before)].Event != kind {
		return fmt.Errorf("update forward event is missing or native history changed; inspect before retrying, no target replay")
	}
	return nil
}

func (a ActionJournal) validateUpdateObservation() error {
	o := a.UpdateObservation
	if o == nil {
		return nil
	}
	if a.UpdateIntent == nil || a.Before == nil || a.Phase == "prepared" || a.After == nil || o.State == nil || o.Actor == "" || o.ObservedAt.IsZero() || o.Proof != forwardObservationProof || !reflect.DeepEqual(o.State, a.After) {
		return fmt.Errorf("update observation requires its immutable input and observed after-state")
	}
	probe := ActionJournal{OperationID: "probe", SessionID: o.SessionID, IssueID: "gh-1", Action: "create", Phase: "prepared"}
	if err := probe.validate(); err != nil {
		return err
	}
	if (a.UpdateIntent.NativeEvents != nil) != (o.NativeEvents != nil) {
		return fmt.Errorf("update observation differs from recorded event evidence")
	}
	var events []nativeStateEvent
	if o.NativeEvents != nil {
		events = *o.NativeEvents
	}
	return updateEventProof(a, events)
}

func (c *Client) observeForwardUpdate(ctx context.Context, a *ActionJournalRecord) (*Record, []nativeStateEvent, error) {
	if a.UpdateIntent == nil || (a.Phase != "prepared" && a.Phase != "applied") {
		return nil, nil, &UndoScopeError{Reason: "forward recovery requires the original recorded update input; no target replay"}
	}
	snapshot, err := c.ReadSnapshot(WithFreshSnapshot(ctx), true)
	if err != nil {
		return nil, nil, err
	}
	history, err := snapshot.ActionHistory("")
	if err != nil {
		return nil, nil, err
	}
	carriers, receipts := 0, 0
	var current *Record
	all := map[string]Record{}
	for i, record := range snapshot.records {
		if record.meta.EntityKind == "journal" {
			carriers++
			if record.ID != a.CarrierID {
				return nil, nil, fmt.Errorf("forward recovery carrier is ambiguous")
			}
		}
		if record.ID == a.IssueID {
			current = &snapshot.records[i]
		}
	}
	for _, record := range snapshot.Issues(true) {
		all[record.ID] = record
	}
	for _, receipt := range history {
		if receipt.ID == a.ID {
			receipts++
			if !reflect.DeepEqual(receipt, *a) {
				return nil, nil, fmt.Errorf("forward recovery receipt changed during observation")
			}
		}
	}
	if carriers != 1 || receipts != 1 || current == nil {
		return nil, nil, fmt.Errorf("forward recovery needs one carrier, its exact receipt and the native target")
	}
	state, err := actionIssueState(current)
	if err != nil {
		return nil, nil, err
	}
	want, err := a.UpdateIntent.Target.contents()
	if err != nil {
		return nil, nil, err
	}
	if !sameActionContents(want, state) || state.UpdatedAt.Before(a.Before.UpdatedAt) || a.After != nil && a.After.Revision != state.Revision {
		return nil, nil, &ConflictError{ID: a.IssueID}
	}
	for _, related := range a.UpdateIntent.Related {
		record, found := all[related.ID]
		if !found {
			return nil, nil, &ConflictError{ID: related.ID}
		}
		now, err := actionIssueState(&record)
		if err != nil || now.Revision != related.State.Revision {
			return nil, nil, &ConflictError{ID: related.ID}
		}
	}
	var events []nativeStateEvent
	if a.UpdateIntent.NativeEvents != nil {
		events, err = c.readStateEvents(withReviewCost(ctx), a.IssueID)
		if err != nil {
			return nil, nil, err
		}
	}
	if err := updateEventProof(a.ActionJournal, events); err != nil {
		return nil, nil, err
	}
	if a.UpdateObservation != nil && a.UpdateObservation.NativeEvents != nil && !reflect.DeepEqual(*a.UpdateObservation.NativeEvents, events) {
		return nil, nil, &ConflictError{ID: a.IssueID}
	}
	if current.meta.Details != nil {
		d, err := current.CopyDetails()
		if err != nil {
			return nil, nil, err
		}
		if d.ReviewBasis != "" {
			if current.nativeState == "closed" {
				if err := c.verifyClosedApproval(withReviewCost(ctx), current, d); err != nil {
					return nil, nil, err
				}
			} else if d.ReviewBasis != reviewBasis(current, d) || stateEventDigest(events) != d.ReviewEvents {
				return nil, nil, workflowStateError("planned review proof is stale for %s", a.IssueID)
			}
			if err := c.verifyReviewHandoffObserved(withReviewCost(ctx), current, d, d.DeletedAt != nil); err != nil {
				return nil, nil, err
			}
		}
		if d.DeletedAt == nil {
			_, meta, _, _ := decodeBody(a.Before.Body)
			before := IssueDetails{}
			if meta.Details != nil {
				before = *meta.Details
			}
			graph := d
			graph.Dependencies = nil
			for _, id := range d.Dependencies {
				recorded := false
				for _, related := range a.UpdateIntent.Related {
					if related.ID == id {
						recorded = true
					}
				}
				if recorded || !slices.Contains(before.Dependencies, id) {
					graph.Dependencies = append(graph.Dependencies, id)
				}
			}
			if before.ParentID == d.ParentID {
				graph.ParentID = ""
			}
			if graph.ParentID != "" || len(graph.Dependencies) > 0 {
				meta = current.meta
				meta.Details = &graph
				body, err := encodeBody(current.Description, meta)
				if err != nil {
					return nil, nil, err
				}
				proposed := state
				proposed.Body = body
				proposal := &ActionJournalRecord{ActionJournal: ActionJournal{IssueID: a.IssueID, Before: &proposed}}
				if _, err := relationUndoGraph(proposal, current, all, false); err != nil {
					return nil, nil, err
				}
			}
		}
	}
	if current.meta.Board != nil && current.meta.Board.DeletedAt == nil {
		_, id := a.EntityIdentity()
		if err := c.checkBoardName(ctx, current.Title, id); err != nil {
			return nil, nil, err
		}
	}
	point, err := c.readJournalTarget(withReadbackCost(ctx), a.IssueID)
	if err != nil {
		return nil, nil, err
	}
	if point.revision != current.revision {
		return nil, nil, &ConflictError{ID: a.IssueID}
	}
	return point, events, nil
}

func (c *Client) reconcileForwardUpdate(ctx context.Context, a *ActionJournalRecord, session, actor string, apply bool) (*ActionReconcileResult, error) {
	current, events, err := c.observeForwardUpdate(ctx, a)
	if err != nil {
		return nil, err
	}
	outcome := &ActionUndoOutcome{Proof: forwardObservationProof}
	if a.Phase == "applied" && a.UpdateObservation == nil {
		outcome = &ActionUndoOutcome{Proof: "verified-forward-receipt", TargetWriteVerified: true, CausalUndoChainEligible: a.Before.State == a.After.State}
	}
	result := &ActionReconcileResult{Issue: current, Journal: a, AlreadyReconciled: a.Phase == "applied", ForwardOutcome: outcome}
	if a.Phase == "applied" || !apply {
		return result, nil
	}
	oldBody, err := renderExistingActionJournal(a)
	if err != nil {
		return nil, err
	}
	number, err := CommentNumber(a.ID)
	if err != nil {
		return nil, err
	}
	path := fmt.Sprintf("/issues/comments/%d", number)
	original, err := c.journalComment(ctx, path, nil, oldBody, a.CarrierID)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(original, a) {
		return nil, fmt.Errorf("forward recovery original receipt changed; no target replay")
	}
	checked, checkedEvents, err := c.observeForwardUpdate(ctx, a)
	if err != nil {
		return nil, err
	}
	if checked.revision != current.revision || !reflect.DeepEqual(checkedEvents, events) {
		return nil, &ConflictError{ID: a.IssueID}
	}
	state, err := actionIssueState(current)
	if err != nil {
		return nil, err
	}
	observation := &ActionUpdateObservation{SessionID: session, Actor: actor, ObservedAt: time.Now().UTC(), Proof: forwardObservationProof, State: &state}
	if a.UpdateIntent.NativeEvents != nil {
		observation.NativeEvents = &events
	}
	resolved := a.ActionJournal
	resolved.Phase, resolved.After, resolved.UpdateObservation = "applied", &state, observation
	body, err := renderWriteActionJournal(resolved)
	if err != nil {
		return nil, err
	}
	if fence, ok := ctx.Value(orderedReceiptFenceContextKey{}).(func() error); ok {
		if err := fence(); err != nil {
			return nil, err
		}
	}
	written, err := c.journalComment(ctx, path, map[string]string{"body": body}, body, a.CarrierID)
	if err != nil {
		return nil, fmt.Errorf("forward recovery receipt is unverified; inspect before retrying, no target replay: %w", err)
	}
	if !sameJournalIdentity(a, written) || !strings.EqualFold(written.Author, actor) {
		return nil, fmt.Errorf("forward recovery receipt identity changed; no target replay")
	}
	verified, err := c.journalComment(withReadbackCost(ctx), path, nil, body, a.CarrierID)
	if err != nil {
		return nil, fmt.Errorf("forward recovery receipt readback is unverified; no target replay: %w", err)
	}
	if !reflect.DeepEqual(written, verified) {
		return nil, fmt.Errorf("forward recovery receipt changed during readback")
	}
	final, _, err := c.observeForwardUpdate(ctx, verified)
	if err != nil {
		return nil, fmt.Errorf("forward recovery receipt saved but final evidence failed; no target replay: %w", err)
	}
	if final.revision != current.revision {
		return nil, &ConflictError{ID: a.IssueID, AfterWrite: true}
	}
	result.Journal, result.Applied = verified, true
	return result, nil
}

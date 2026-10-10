package ghstore

import (
	"context"
	"crypto/rand"
	"fmt"
	"reflect"
	"strings"
	"time"
)

// Check historical evidence and event baseline before the final source point.
// Native writes are optimistic; GitHub does not supply an atomic issue CAS.
func (c *Client) checkNativeUndoFence(ctx context.Context, a *ActionJournalRecord, n *ActionNativeUndo, current *Record, after bool) ([]nativeStateEvent, error) {
	events, err := c.readStateEvents(withReviewCost(ctx), a.IssueID)
	if err != nil {
		return nil, err
	}
	if err := nativeUndoEventProof(n, events, after); err != nil {
		return nil, err
	}
	if after && len(n.EventsAfter) != 0 && !reflect.DeepEqual(n.EventsAfter, events) {
		return nil, &ConflictError{ID: a.IssueID, AfterWrite: true}
	}
	if err := nativeUndoHistoricalReview(a, n); err != nil {
		return nil, fmt.Errorf("native undo historical review is unverified: %w", err)
	}
	before, err := actionStateRecord(a.Before, a.IssueID)
	if err != nil {
		return nil, err
	}
	d, err := before.CopyDetails()
	if err != nil {
		return nil, err
	}
	if d.ReviewBasis != "" {
		if err := c.verifyReviewHandoffObserved(withReviewCost(ctx), before, d, false); err != nil {
			return nil, fmt.Errorf("native undo original handoff proof failed: %w", err)
		}
	}
	latest, err := c.readJournalTarget(ctx, a.IssueID)
	if err != nil {
		return nil, err
	}
	if latest.revision != current.revision {
		return nil, &ConflictError{ID: a.IssueID, AfterWrite: after}
	}
	return events, nil
}

func (c *Client) undoNativeAction(ctx context.Context, a *ActionJournalRecord, current *Record, session, actor string) (*ActionUndoResult, error) {
	if a.SessionID != session || !strings.EqualFold(a.Author, actor) {
		return nil, fmt.Errorf("native undo is not attributed to the current session and GitHub author")
	}
	if a.Phase == "undo_prepared" {
		return nil, fmt.Errorf("native inverse is uncertain; inspect with undo --reconcile, no target replay attempted")
	}
	if a.Phase == "undone" {
		if a.NativeUndo == nil {
			return nil, fmt.Errorf("native compensation has no verified intent")
		}
		state, err := actionIssueState(current)
		if err != nil {
			return nil, err
		}
		if state.Revision != a.Restored.Revision {
			return nil, &ConflictError{ID: a.IssueID}
		}
		if _, err := c.checkNativeUndoFence(ctx, a, a.NativeUndo, current, true); err != nil {
			return nil, err
		}
		return &ActionUndoResult{Issue: current, Journal: a, AlreadyUndone: true, Compensation: &a.NativeUndo.Plan, UndoOutcome: nativeUndoOutcome(a.NativeUndo)}, nil
	}
	plan, err := a.PlanNativeIssueUndo(current, session, actor, "td-op-"+rand.Text(), time.Now().UTC())
	if err != nil {
		return nil, err
	}
	baseline, err := c.readStateEvents(withReviewCost(ctx), a.IssueID)
	if err != nil {
		return nil, err
	}
	native := &ActionNativeUndo{Plan: *plan, Baseline: baseline}
	if _, err := c.checkNativeUndoFence(ctx, a, native, current, false); err != nil {
		return nil, err
	}
	pending := a.ActionJournal
	pending.Phase = "undo_prepared"
	pending.UndoOperationID = plan.UndoOperationID
	pending.NativeUndo = native
	prepared, err := c.rewriteActionJournalGuarded(ctx, a, pending, func() error { _, err := c.checkNativeUndoFence(ctx, a, native, current, false); return err })
	if err != nil {
		return nil, fmt.Errorf("native undo intent is unverified; no target write attempted: %w", err)
	}
	if _, err := c.checkNativeUndoFence(ctx, a, native, current, false); err != nil {
		return nil, err
	}
	data, err := c.request(ctx, "PATCH", fmt.Sprintf("/issues/%d", current.Number), plan.Payload, false)
	if err != nil {
		return nil, fmt.Errorf("native undo %s has an uncertain result; inspect with --reconcile before retrying, no automatic replay: %w", plan.UndoOperationID, err)
	}
	restored, err := decodeIssue(data)
	if err != nil {
		return nil, fmt.Errorf("native undo response unreadable; inspect before retrying: %w", err)
	}
	state, err := actionIssueState(restored)
	if err != nil {
		return nil, err
	}
	expected, err := nativePlanContents(*plan)
	if err != nil {
		return nil, err
	}
	if restored.ID != current.ID || !sameActionContents(expected, state) || state.UpdatedAt.Before(a.After.UpdatedAt) {
		return nil, fmt.Errorf("native undo response differs from compensation; inspect before retrying")
	}
	restored.repository = c.repo
	verified, err := c.readJournalTarget(withReadbackCost(ctx), current.ID)
	if err != nil {
		return nil, fmt.Errorf("native inverse was accepted but source readback failed: %w", err)
	}
	if verified.revision != restored.revision {
		return nil, &ConflictError{ID: current.ID, AfterWrite: true}
	}
	events, err := c.checkNativeUndoFence(ctx, a, native, restored, true)
	if err != nil {
		return nil, fmt.Errorf("native inverse was accepted but event/proof verification failed; inspect with --reconcile, no target replay: %w", err)
	}
	completion := *native
	completion.Proof = "verified-native-inverse"
	completion.EventsAfter = events
	done := prepared.ActionJournal
	done.Phase = "undone"
	done.Restored = &state
	done.NativeUndo = &completion
	receipt, err := c.rewriteActionJournalGuarded(ctx, prepared, done, func() error { _, err := c.checkNativeUndoFence(ctx, a, &completion, restored, true); return err })
	if err != nil {
		return nil, fmt.Errorf("native inverse was verified but completion receipt is unverified; inspect with --reconcile: %w", err)
	}
	if _, err := c.checkNativeUndoFence(ctx, a, &completion, restored, true); err != nil {
		return nil, fmt.Errorf("native receipt saved but final proof changed; inspect before retrying: %w", err)
	}
	return &ActionUndoResult{Issue: restored, Journal: receipt, Compensation: &receipt.NativeUndo.Plan, UndoOutcome: nativeUndoOutcome(receipt.NativeUndo)}, nil
}

func (c *Client) observeNativeUndo(ctx context.Context, a *ActionJournalRecord) (*Record, []nativeStateEvent, error) {
	snapshot, err := c.ReadSnapshot(WithFreshSnapshot(ctx), true)
	if err != nil {
		return nil, nil, err
	}
	history, err := snapshot.ActionHistory("")
	if err != nil {
		return nil, nil, err
	}
	carriers, receipts := 0, 0
	for _, record := range snapshot.records {
		if record.meta.EntityKind == "journal" {
			carriers++
			if record.ID != a.CarrierID {
				return nil, nil, fmt.Errorf("native recovery carrier is ambiguous")
			}
		}
	}
	for _, receipt := range history {
		if receipt.ID == a.ID {
			receipts++
			if !reflect.DeepEqual(receipt, *a) {
				return nil, nil, fmt.Errorf("native recovery receipt changed")
			}
		}
	}
	if carriers != 1 || receipts != 1 {
		return nil, nil, fmt.Errorf("native recovery requires one carrier and its exact receipt")
	}
	var current *Record
	for i := range snapshot.records {
		if snapshot.records[i].ID == a.IssueID {
			current = &snapshot.records[i]
		}
	}
	if current == nil {
		return nil, nil, fmt.Errorf("native recovery target not observed")
	}
	state, err := actionIssueState(current)
	if err != nil {
		return nil, nil, err
	}
	contents, err := nativePlanContents(a.NativeUndo.Plan)
	if err != nil {
		return nil, nil, err
	}
	if !sameActionContents(contents, state) || state.UpdatedAt.Before(a.After.UpdatedAt) || a.Phase == "undone" && state.Revision != a.Restored.Revision {
		return nil, nil, &ConflictError{ID: a.IssueID}
	}
	events, err := c.checkNativeUndoFence(ctx, a, a.NativeUndo, current, true)
	if err != nil {
		return nil, nil, err
	}
	current.repository = c.repo
	return current, events, nil
}

func (c *Client) reconcileNativeUndo(ctx context.Context, a *ActionJournalRecord, session, actor string, apply bool) (*ActionReconcileResult, error) {
	if !strings.EqualFold(actor, a.Author) {
		return nil, fmt.Errorf("native recovery actor differs from receipt author")
	}
	current, events, err := c.observeNativeUndo(ctx, a)
	if err != nil {
		return nil, err
	}
	outcome := nativeUndoOutcome(a.NativeUndo)
	if outcome.Proof == "" {
		outcome.Proof = inverseObservationProof
	}
	result := &ActionReconcileResult{Issue: current, Journal: a, AlreadyReconciled: a.Phase == "undone", UndoOutcome: outcome, Compensation: &a.NativeUndo.Plan}
	if a.Phase == "undone" || !apply {
		return result, nil
	}
	// rewriteActionJournal re-reads the exact original receipt before saving it.
	// A second proof/source fence immediately precedes that journal-only write.
	if _, err := c.checkNativeUndoFence(ctx, a, a.NativeUndo, current, true); err != nil {
		return nil, err
	}
	state, err := actionIssueState(current)
	if err != nil {
		return nil, err
	}
	observed := *a.NativeUndo
	observed.Proof = inverseObservationProof
	observed.EventsAfter = events
	observed.ObserverSession = session
	observed.ObservedAt = time.Now().UTC()
	done := a.ActionJournal
	done.Phase = "undone"
	done.Restored = &state
	done.NativeUndo = &observed
	receipt, err := c.rewriteActionJournalGuarded(ctx, a, done, func() error { _, err := c.checkNativeUndoFence(ctx, a, &observed, current, true); return err })
	if err != nil {
		return nil, fmt.Errorf("native recovery receipt is unverified; no target replay: %w", err)
	}
	verified, _, err := c.observeNativeUndo(ctx, receipt)
	if err != nil {
		return nil, fmt.Errorf("native recovery receipt saved but observation failed; no target replay: %w", err)
	}
	if verified.revision != current.revision {
		return nil, &ConflictError{ID: a.IssueID}
	}
	result.Journal = receipt
	result.Applied = true
	result.UndoOutcome = nativeUndoOutcome(receipt.NativeUndo)
	return result, nil
}

package ghstore

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
)

func orderedAppliedEffects(a *ActionJournalRecord) []*ActionJournalRecord {
	effects := []*ActionJournalRecord{}
	for _, effect := range a.Group.Ordered.Forward {
		if effect.Status != "unapplied" {
			effects = append(effects, effect.Record)
		}
	}
	return effects
}

// Historical handoff clocks are independent of task revisions. Check them
// against original Before records, never a rebased compensation checkpoint.
func (c *Client) checkOrderedHistoricalHandoffs(ctx context.Context, a *ActionJournalRecord, observed *orderedGroupObservation) error {
	for _, effect := range orderedAppliedEffects(a) {
		original, err := actionStateRecord(effect.Before, effect.IssueID)
		if err != nil {
			return err
		}
		details, err := original.CopyDetails()
		if err != nil {
			return err
		}
		if details.ReviewBasis != "" {
			if workflowHandoffRemovalObserved(a, effect, &details, observed) {
				continue
			}
			if err := c.verifyReviewHandoffObserved(withReviewCost(ctx), original, details, false); err != nil {
				return fmt.Errorf("ordered original handoff proof failed: %w", err)
			}
		}
	}
	return nil
}

func (c *Client) fenceOrderedInverse(ctx context.Context, a *ActionJournalRecord, observed *orderedGroupObservation) error {
	if err := preflightWorkflowInverseGraph(a, observed); err != nil {
		return err
	}
	if err := c.checkOrderedHistoricalHandoffs(ctx, a, observed); err != nil {
		return err
	}
	return c.fenceOrderedObservation(ctx, a, observed, false)
}

// This is a private sizing projection, not an observed result or future intent.
// It plans the full remaining reverse sequence, including growing audit/event
// payloads, and reserves per-step clock/identity headroom in both byte budgets.
func checkOrderedInverseCapacity(a *ActionJournalRecord, observed *orderedGroupObservation, undo string) error {
	probe := copyOrderedJournal(a)
	probe.Phase, probe.UndoOperationID = "undo_prepared", undo
	group := *a
	group.ActionJournal = probe
	effects := orderedAppliedEffects(a)
	states := map[string]ActionIssueState{}
	events := map[string][]nativeStateEvent{}
	for _, member := range observed.Members {
		states[member.IssueID] = member.State
	}
	for id, baseline := range observed.Events {
		events[id] = slices.Clone(baseline)
	}
	remaining := len(effects) - len(probe.Group.Ordered.Steps)
	if remaining < 0 || observed.PendingStep {
		return fmt.Errorf("ordered capacity needs completed existing steps")
	}
	for len(probe.Group.Ordered.Steps) < len(effects) {
		index := len(effects) - len(probe.Group.Ordered.Steps) - 1
		id := effects[index].IssueID
		at := time.Now().UTC()
		if !at.After(states[id].UpdatedAt) {
			at = states[id].UpdatedAt.Add(time.Nanosecond)
		}
		for _, step := range probe.Group.Ordered.Steps {
			if !at.After(step.At) {
				at = step.At.Add(time.Nanosecond)
			}
		}
		step, err := PlanOrderedGroupUndoStep(&group, effects, a.Group.Ordered.Events, probe.Group.Ordered.Steps, states[id], events[id], undo, "td-op-"+rand.Text(), a.SessionID, a.Author, at)
		if err != nil {
			return err
		}
		state, err := step.Input.contents()
		if err != nil {
			return err
		}
		state.UpdatedAt = at.Add(time.Nanosecond)
		state.Revision, err = state.checksum()
		if err != nil {
			return err
		}
		nextEvents := slices.Clone(events[id])
		if step.Source.State != step.Input.State {
			last := int64(0)
			if len(nextEvents) > 0 {
				last = nextEvents[len(nextEvents)-1].ID
			}
			if last == math.MaxInt64 {
				return fmt.Errorf("ordered native event identity cannot advance")
			}
			kind := "closed"
			if step.Input.State == "open" {
				kind = "reopened"
			}
			nextEvents = append(nextEvents, nativeStateEvent{ID: last + 1, Event: kind})
		}
		step.Result, step.EventsAfter, step.Proof = &state, &nextEvents, "verified-group-inverse"
		probe.Group.Ordered.Steps = append(probe.Group.Ordered.Steps, step)
		states[id], events[id] = state, nextEvents
	}
	inverse := *a.Group.Forward
	inverse.SessionID, inverse.Actor, inverse.ObservedAt = a.SessionID, a.Author, time.Now().UTC()
	inverse.Members = slices.Clone(inverse.Members)
	for i, member := range inverse.Members {
		inverse.Members[i].State = states[member.IssueID]
		for _, step := range probe.Group.Ordered.Steps {
			for _, effect := range effects {
				if effect.ID == step.ForwardReceiptID && effect.IssueID == member.IssueID {
					inverse.Members[i].Status = "undone"
				}
			}
			if step.Proof == inverseObservationProof {
				inverse.Proof = "group-contents-observed"
			}
		}
	}
	probe.Phase, probe.Group.Inverse = "undone", &inverse
	headroom := 512 * (remaining + 1)
	return checkJournalSizingHeadroom(probe, headroom)
}

func (c *Client) undoOrderedGroup(ctx context.Context, a *ActionJournalRecord, session string) (*ActionUndoResult, error) {
	if a.SessionID != session {
		return nil, fmt.Errorf("ordered undo requires its original session")
	}
	if a.Phase == "prepared" {
		return nil, fmt.Errorf("ordered forward result is unverified; reconcile before undo, no target write attempted")
	}
	if _, err := c.journalCarrier(ctx, a.CarrierID); err != nil {
		return nil, err
	}
	profile, err := c.requestAPI(ctx, "GET", "user", "/user", nil, false)
	if err != nil {
		return nil, err
	}
	var user struct{ Login string }
	if json.Unmarshal(profile, &user) != nil || user.Login == "" || !strings.EqualFold(user.Login, a.Author) {
		return nil, fmt.Errorf("ordered undo requires its authenticated native author")
	}
	observed, err := c.observeOrderedGroup(ctx, a, false)
	if err != nil {
		return nil, err
	}
	if observed.PendingStep {
		return nil, fmt.Errorf("ordered inverse is uncertain; reconcile its receipt before continuing, no target replay")
	}
	if err := c.fenceOrderedInverse(ctx, a, observed); err != nil {
		return nil, err
	}
	root := observed.Records[a.IssueID]
	if a.Phase == "undone" {
		return &ActionUndoResult{Issue: &root, Journal: a, AlreadyUndone: true, GroupMembers: observed.Members, UndoOutcome: groupResultOutcome(a, true)}, nil
	}
	undo := a.UndoOperationID
	if undo == "" {
		undo = "td-op-" + rand.Text()
	}
	if err := checkOrderedInverseCapacity(a, observed, undo); err != nil {
		return nil, fmt.Errorf("ordered inverse preflight (no target write attempted): %w", err)
	}
	if a.Phase == "applied" {
		next := copyOrderedJournal(a)
		next.Phase, next.UndoOperationID = "undo_prepared", undo
		a, err = c.saveOrderedInverseReceipt(ctx, a, next, observed)
		if err != nil {
			return nil, err
		}
		observed, err = c.observeOrderedGroup(ctx, a, false)
		if err != nil {
			return nil, err
		}
	}
	effects := orderedAppliedEffects(a)
	for len(a.Group.Ordered.Steps) < len(effects) {
		effect := effects[len(effects)-len(a.Group.Ordered.Steps)-1]
		current := observed.Records[effect.IssueID]
		source, err := actionIssueState(&current)
		if err != nil {
			return nil, err
		}
		step, err := PlanOrderedGroupUndoStep(a, effects, a.Group.Ordered.Events, a.Group.Ordered.Steps, source, observed.Events[effect.IssueID], undo, "td-op-"+rand.Text(), session, user.Login, time.Now().UTC())
		if err != nil {
			return nil, err
		}
		next := copyOrderedJournal(a)
		next.Group.Ordered.Steps = append(next.Group.Ordered.Steps, step)
		a, err = c.saveOrderedInverseReceipt(ctx, a, next, observed)
		if err != nil {
			return nil, fmt.Errorf("ordered inverse input unverified; no target write attempted: %w", err)
		}
		observed, err = c.observeOrderedGroup(ctx, a, false)
		if err != nil {
			return nil, err
		}
		if err := c.fenceOrderedInverse(ctx, a, observed); err != nil {
			return nil, err
		}
		point, err := c.readJournalTarget(withReadbackCost(ctx), effect.IssueID)
		if err != nil || point.revision != current.revision {
			if err != nil {
				return nil, err
			}
			return nil, &ConflictError{ID: effect.IssueID}
		}
		var result *Record
		if step.Comment != nil {
			if err := c.readNativeComment(ctx, step.Comment); err != nil {
				return nil, err
			}
			n, err := CommentNumber(step.Comment.ID)
			if err != nil {
				return nil, err
			}
			if _, err := c.request(ctx, "DELETE", fmt.Sprintf("/issues/comments/%d", n), nil, false); err != nil {
				return nil, fmt.Errorf("ordered companion deletion has an uncertain result; reconcile without replay: %w", err)
			}
			if err := c.verifyDeletedComment(ctx, effect); err != nil {
				return nil, err
			}
			result, err = c.readJournalTarget(withReadbackCost(ctx), effect.IssueID)
			if err != nil {
				return nil, err
			}
		} else {
			payload := map[string]any{"title": step.Input.Title, "body": step.Input.Body, "state": step.Input.State, "labels": step.Input.Labels}
			data, err := c.request(ctx, "PATCH", fmt.Sprintf("/issues/%d", current.Number), payload, false)
			if err != nil {
				return nil, fmt.Errorf("ordered inverse %s has an uncertain result; inspect and reconcile before continuing, no automatic replay: %w", step.OperationID, err)
			}
			result, err = decodeIssue(data)
			if err != nil {
				return nil, fmt.Errorf("ordered inverse response unreadable; no automatic replay: %w", err)
			}
		}
		state, err := actionIssueState(result)
		if err != nil {
			return nil, err
		}
		want, err := step.Input.contents()
		if err != nil || result.ID != effect.IssueID || !sameActionContents(want, state) || state.UpdatedAt.Before(source.UpdatedAt) {
			return nil, fmt.Errorf("ordered inverse response differs from its original input; inspect before continuing")
		}
		point, err = c.readJournalTarget(withReadbackCost(ctx), effect.IssueID)
		if err != nil {
			return nil, fmt.Errorf("ordered inverse accepted but readback failed; reconcile without replay: %w", err)
		}
		if point.revision != result.revision {
			return nil, &ConflictError{ID: effect.IssueID, AfterWrite: true}
		}
		observed, err = c.observeOrderedGroup(ctx, a, false)
		if err != nil {
			return nil, fmt.Errorf("ordered inverse accepted but group evidence failed; reconcile without replay: %w", err)
		}
		if !observed.PendingApplied || observed.Records[effect.IssueID].revision != result.revision {
			return nil, &ConflictError{ID: effect.IssueID, AfterWrite: true}
		}
		done := *step
		events := slices.Clone(observed.Events[effect.IssueID])
		done.Result, done.EventsAfter, done.Proof = &state, &events, "verified-group-inverse"
		next = copyOrderedJournal(a)
		next.Group.Ordered.Steps[len(next.Group.Ordered.Steps)-1] = &done
		a, err = c.saveOrderedInverseReceipt(ctx, a, next, observed)
		if err != nil {
			return nil, fmt.Errorf("ordered inverse verified but receipt unverified; reconcile without target replay: %w", err)
		}
		observed, err = c.observeOrderedGroup(ctx, a, false)
		if err != nil {
			return nil, err
		}
	}
	completed, err := c.reconcileOrderedGroup(ctx, a, session, user.Login, true, true)
	if err != nil {
		return nil, err
	}
	return &ActionUndoResult{Issue: completed.Issue, Journal: completed.Journal, GroupMembers: completed.GroupMembers, UndoOutcome: groupResultOutcome(completed.Journal, true)}, nil
}

func (c *Client) saveOrderedInverseReceipt(ctx context.Context, a *ActionJournalRecord, next ActionJournal, observed *orderedGroupObservation) (*ActionJournalRecord, error) {
	written, err := c.writeGroupReceipt(ctx, a, next, func() error {
		return c.fenceOrderedInverse(ctx, a, observed)
	})
	if err != nil {
		return nil, err
	}
	if _, err := c.observeOrderedGroup(ctx, written, false); err != nil {
		return nil, fmt.Errorf("ordered inverse receipt saved but observation failed; inspect before continuing: %w", err)
	}
	return written, nil
}

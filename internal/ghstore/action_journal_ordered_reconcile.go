package ghstore

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"
)

type orderedReceiptFenceContextKey struct{}

func sameOrderedObservation(a, b *orderedGroupObservation) bool {
	return equalGroupMemberResults(a.Members, b.Members) &&
		reflect.DeepEqual(a.Effects, b.Effects) && reflect.DeepEqual(a.Events, b.Events) &&
		reflect.DeepEqual(a.Comments, b.Comments) &&
		a.PendingReceipt == b.PendingReceipt && a.PendingStep == b.PendingStep &&
		a.PendingApplied == b.PendingApplied && a.Remaining == b.Remaining && a.Partial == b.Partial
}

func (c *Client) fenceOrderedObservation(ctx context.Context, a *ActionJournalRecord, observed *orderedGroupObservation, normal bool) error {
	if err := c.fenceWorkflowHandoffs(ctx, a); err != nil {
		return err
	}
	if err := c.fenceWorkflowCompanions(ctx, observed); err != nil {
		return err
	}
	for _, effect := range observed.Effects {
		if err := c.pointOrderedEffect(ctx, effect); err != nil {
			return err
		}
	}
	fresh, err := c.observeOrderedGroup(ctx, a, normal)
	if err != nil {
		return err
	}
	if !sameOrderedObservation(observed, fresh) {
		return fmt.Errorf("ordered group changed at receipt fence; no target replay")
	}
	return nil
}

// Separate ownership of nested mutable structures keeps original forward
// snapshots and completed compensation provenance immutable across writes.
func copyOrderedJournal(a *ActionJournalRecord) ActionJournal {
	next := a.ActionJournal
	g := *a.Group
	o := *g.Ordered
	o.Steps = slices.Clone(o.Steps)
	g.Ordered, next.Group = &o, &g
	return next
}

func orderedObservationProof(observed *orderedGroupObservation, normal bool) string {
	if !normal {
		return "group-contents-observed"
	}
	for _, effect := range observed.Effects {
		if effect.Status == "observed" {
			return "group-contents-observed"
		}
	}
	return "verified-group-receipts"
}

func (c *Client) saveOrderedObservation(ctx context.Context, a *ActionJournalRecord, next ActionJournal, observed *orderedGroupObservation, normal bool) (*ActionJournalRecord, error) {
	written, err := c.writeGroupReceipt(ctx, a, next, func() error {
		if a.Phase == "undo_prepared" || a.Phase == "undone" {
			return c.fenceOrderedInverse(ctx, a, observed)
		}
		return c.fenceOrderedObservation(ctx, a, observed, normal)
	})
	if err != nil {
		return nil, err
	}
	if _, err := c.observeOrderedGroup(ctx, written, false); err != nil {
		return nil, fmt.Errorf("ordered receipt saved but final observation failed; no target replay: %w", err)
	}
	return written, nil
}

func (c *Client) reconcileOrderedGroup(ctx context.Context, a *ActionJournalRecord, session, actor string, apply, normal bool) (*ActionReconcileResult, error) {
	if !strings.EqualFold(a.Author, actor) {
		return nil, fmt.Errorf("ordered reconciliation requires its native author")
	}
	if err := orderedUndoIdentity("check-observer", session, a.IssueID); err != nil {
		return nil, err
	}
	observed, err := c.observeOrderedGroup(ctx, a, normal)
	if err != nil {
		return nil, err
	}
	root := observed.Records[a.IssueID]
	result := &ActionReconcileResult{Journal: a, Issue: &root, GroupMembers: observed.Members, AlreadyReconciled: a.Phase == "applied" || a.Phase == "undone", TargetWriteReplayed: false}
	inverse := a.Phase == "undo_prepared" || a.Phase == "undone"
	if inverse {
		result.UndoOutcome = groupResultOutcome(a, true)
	} else {
		result.ForwardOutcome = groupResultOutcome(a, false)
	}
	if result.AlreadyReconciled || !apply {
		return result, nil
	}
	if observed.PendingReceipt != "" {
		var member *ActionJournalRecord
		for _, effect := range observed.Effects {
			if effect.Record.ID == observed.PendingReceipt {
				member = effect.Record
			}
		}
		if member == nil {
			return nil, fmt.Errorf("ordered pending forward receipt is missing")
		}
		// Existing forward recovery retains the original intent and attributes
		// only the observation. The additional gate fences the entire group
		// immediately before its receipt PATCH, including all original bodies.
		guarded := context.WithValue(ctx, orderedReceiptFenceContextKey{}, func() error {
			return c.fenceOrderedObservation(ctx, a, observed, false)
		})
		var recoveryErr error
		if member.CommentInput != nil {
			_, recoveryErr = c.reconcileForwardComment(guarded, member, session, actor, true)
		} else {
			_, recoveryErr = c.reconcileForwardUpdate(guarded, member, session, actor, true)
		}
		if recoveryErr != nil {
			return nil, fmt.Errorf("ordered forward receipt recovery stopped; no target replay: %w", recoveryErr)
		}
		result.Applied = true
		observed, err = c.observeOrderedGroup(ctx, a, false)
		if err != nil {
			return nil, err
		}
	}
	if observed.PendingStep {
		if !observed.PendingApplied {
			return nil, fmt.Errorf("ordered inverse input is not observed as applied; inspect before continuing, no target or receipt replay")
		}
		next := copyOrderedJournal(a)
		index := len(next.Group.Ordered.Steps) - 1
		step := *next.Group.Ordered.Steps[index]
		id := ""
		for _, effect := range observed.Effects {
			if effect.Record.ID == step.ForwardReceiptID {
				id = effect.Record.IssueID
			}
		}
		record, found := observed.Records[id]
		if !found {
			return nil, fmt.Errorf("ordered inverse target is missing")
		}
		state, err := actionIssueState(&record)
		if err != nil {
			return nil, err
		}
		events := slices.Clone(observed.Events[id])
		step.Result, step.EventsAfter, step.Proof = &state, &events, inverseObservationProof
		step.ObserverSession, step.ObservedAt = session, time.Now().UTC()
		next.Group.Ordered.Steps[index] = &step
		a, err = c.saveOrderedObservation(ctx, a, next, observed, false)
		if err != nil {
			return nil, err
		}
		result.Journal, result.Applied, result.GroupReceiptSaved = a, true, true
		observed, err = c.observeOrderedGroup(ctx, a, false)
		if err != nil {
			return nil, err
		}
	}
	result.GroupMembers = observed.Members
	if inverse && observed.Remaining {
		return result, nil
	}
	next := copyOrderedJournal(a)
	observation := &ActionGroupObservation{SessionID: session, Actor: actor, ObservedAt: time.Now().UTC(), Proof: orderedObservationProof(observed, normal), Partial: observed.Partial, Members: observed.Members}
	if inverse {
		for _, step := range a.Group.Ordered.Steps {
			if step.Proof == inverseObservationProof {
				observation.Proof = "group-contents-observed"
			}
		}
		next.Phase, next.Group.Inverse = "undone", observation
	} else {
		next.Phase, next.Group.Forward = "applied", observation
		next.Group.Ordered.Forward, next.Group.Ordered.Events = observed.Effects, observed.Events
	}
	written, err := c.saveOrderedObservation(ctx, a, next, observed, normal)
	if err != nil {
		return nil, err
	}
	result.Journal, result.Applied, result.GroupReceiptSaved = written, true, true
	if inverse {
		result.UndoOutcome = groupResultOutcome(written, true)
	} else {
		result.ForwardOutcome = groupResultOutcome(written, false)
	}
	return result, nil
}

package ghstore

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"
)

func sameUpdateActivityObservation(a, b *updateActivityObservation) bool {
	return a.State.Revision == b.State.Revision && reflect.DeepEqual(a.Receipts, b.Receipts) && reflect.DeepEqual(a.Comment, b.Comment) && equalGroupMemberResults(a.Members, b.Members)
}

func (c *Client) reconcileUpdateActivity(ctx context.Context, root *ActionJournalRecord, session, actor string, apply, normal bool) (*ActionReconcileResult, error) {
	if !strings.EqualFold(root.Author, actor) {
		return nil, fmt.Errorf("update/activity recovery requires its native author")
	}
	observed, err := c.observeUpdateActivity(ctx, root, normal)
	if err != nil {
		return nil, err
	}
	result := &ActionReconcileResult{Journal: root, Issue: observed.Current, GroupMembers: observed.Members, TargetWriteReplayed: false,
		AlreadyReconciled: root.Phase == "applied" || root.Phase == "undone"}
	inverse := root.Phase == "undo_prepared" || root.Phase == "undone"
	if inverse {
		result.UndoOutcome = groupResultOutcome(root, true)
	} else {
		result.ForwardOutcome = groupResultOutcome(root, false)
	}
	if !apply || result.AlreadyReconciled {
		return result, nil
	}
	if observed.Pending != nil {
		if err := c.fenceUpdateActivity(ctx, root, observed.Receipts, observed.State, observed.Comment != nil); err != nil {
			return nil, err
		}
		if _, err := c.ReconcileAction(ctx, observed.Pending.ID, session, true); err != nil {
			return nil, fmt.Errorf("update/activity member recovery failed without target replay: %w", err)
		}
		result.Applied = true
		observed, err = c.observeUpdateActivity(ctx, root, false)
		if err != nil {
			return nil, err
		}
		result.Issue, result.GroupMembers = observed.Current, observed.Members
	}
	if inverse && observed.Remaining {
		return result, nil
	}
	observation := &ActionGroupObservation{SessionID: session, Actor: actor, ObservedAt: time.Now().UTC(),
		Proof: "group-contents-observed", Partial: observed.Partial, Members: observed.Members}
	if normal && !observed.Weak {
		observation.Proof = "verified-group-receipts"
	}
	group := *root.Group
	next := root.ActionJournal
	next.Group = &group
	if inverse {
		next.Phase, group.Inverse = "undone", observation
	} else {
		next.Phase, group.Forward = "applied", observation
		input := *group.UpdateActivity
		input.ForwardDigests = []string{}
		for _, receipt := range observed.Receipts {
			digest, err := updateActivityForwardDigest(receipt)
			if err != nil {
				return nil, err
			}
			input.ForwardDigests = append(input.ForwardDigests, digest)
		}
		group.UpdateActivity = &input
	}
	written, err := c.writeGroupReceipt(ctx, root, next, func() error {
		fresh, err := c.observeUpdateActivity(ctx, root, false)
		if err != nil {
			return err
		}
		if !sameUpdateActivityObservation(observed, fresh) {
			return fmt.Errorf("update/activity results changed before receipt publication")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if _, err := c.observeUpdateActivity(ctx, written, false); err != nil {
		return nil, fmt.Errorf("update/activity receipt saved but final observation failed: %w", err)
	}
	result.Journal, result.Applied, result.GroupReceiptSaved = written, true, true
	return result, nil
}

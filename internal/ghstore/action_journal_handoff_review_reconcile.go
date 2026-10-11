package ghstore

import (
	"context"
	"fmt"
	"strings"
	"time"
)

func (c *Client) reconcilePairedGroup(ctx context.Context, root *ActionJournalRecord, session, actor string, apply, normal bool) (*ActionReconcileResult, error) {
	if !strings.EqualFold(root.Author, actor) {
		return nil, fmt.Errorf("paired command recovery requires its native author")
	}
	observed, err := c.observePairedGroup(ctx, root, normal)
	if err != nil {
		return nil, err
	}
	result := &ActionReconcileResult{Journal: root, Issue: observed.Current[root.IssueID], GroupMembers: observed.Members, TargetWriteReplayed: false, AlreadyReconciled: root.Phase == "applied" || root.Phase == "undone"}
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
		if err := c.fencePairedGroup(ctx, root, observed.Receipts, observed.States, observed.present(), observed.Bridges); err != nil {
			return nil, err
		}
		if _, err := c.ReconcileAction(ctx, observed.Pending.ID, session, true); err != nil {
			return nil, fmt.Errorf("paired command member recovery failed without target replay: %w", err)
		}
		result.Applied = true
		observed, err = c.observePairedGroup(ctx, root, false)
		if err != nil {
			return nil, err
		}
		result.Issue, result.GroupMembers = observed.Current[root.IssueID], observed.Members
	}
	if inverse && observed.Remaining {
		return result, nil
	}
	observation := &ActionGroupObservation{SessionID: session, Actor: actor, ObservedAt: time.Now().UTC(), Proof: "group-contents-observed", Partial: observed.Partial, Members: observed.Members}
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
		digests := []string{}
		for _, r := range observed.Receipts {
			digest, err := updateActivityForwardDigest(r)
			if err != nil {
				return nil, err
			}
			digests = append(digests, digest)
		}
		group.setPairedDigests(digests)
	}
	written, err := c.writeGroupReceipt(ctx, root, next, func() error {
		fresh, err := c.observePairedGroup(ctx, root, false)
		if err != nil {
			return err
		}
		if !samePairedGroupObservation(observed, fresh) {
			return fmt.Errorf("paired command results changed before receipt publication")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if _, err := c.observePairedGroup(ctx, written, false); err != nil {
		return nil, fmt.Errorf("paired command receipt saved but final observation failed: %w", err)
	}
	result.Journal, result.Applied, result.GroupReceiptSaved = written, true, true
	return result, nil
}

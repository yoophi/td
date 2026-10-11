package ghstore

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"time"
)

func (c *Client) readCreationChild(ctx context.Context, root *ActionJournalRecord) (*ActionJournalRecord, error) {
	snapshot, err := c.ReadSnapshot(WithFreshSnapshot(ctx), true)
	if err != nil {
		return nil, err
	}
	history, err := snapshot.ActionHistory("")
	if err != nil {
		return nil, err
	}
	found := false
	var child *ActionJournalRecord
	for i := range history {
		a := &history[i]
		if a.ID == root.ID {
			if !sameCreationChildReceipt(root, a) {
				return nil, fmt.Errorf("creation parent receipt changed during observation")
			}
			found = true
		}
		if a.OperationID == root.CreationCommand.RelationOperationID || a.CreationOwner == root.OperationID {
			if child != nil {
				return nil, fmt.Errorf("creation has ambiguous child attempts")
			}
			if err := validateCreationChild(root, a); err != nil {
				return nil, err
			}
			child = a
		}
	}
	if !found {
		return nil, fmt.Errorf("creation original intent disappeared")
	}
	return child, nil
}

func creationForwardOutcome(a *ActionJournalRecord) *ActionUndoOutcome {
	verified := a.CreationCommand.Forward != nil && a.CreationCommand.Forward.Proof == "verified"
	proof := "creation-contents-observed"
	if verified {
		proof = "verified-creation-command"
	}
	return &ActionUndoOutcome{Proof: proof, TargetWriteVerified: verified, CausalUndoChainEligible: false}
}

// Recovery inspects already existing allocation and relationship effects. It
// can link receipts for a partial command, but never creates the missing task,
// adds a missing reverse edge, or upgrades an observed outcome to verified.
func (c *Client) reconcileCreationCommand(ctx context.Context, root *ActionJournalRecord, session, actor string, apply bool) (*ActionReconcileResult, error) {
	if root.Phase != "prepared" && root.Phase != "applied" {
		return c.reconcileCreationInverse(ctx, root, session, actor, apply)
	}
	current, err := c.observeReconcileResult(ctx, root)
	if err != nil {
		return nil, err
	}
	state, err := actionIssueState(current)
	if err != nil {
		return nil, err
	}
	if slices.Contains(root.CreationCommand.OriginalIDs, current.ID) || root.After != nil && state.Revision != root.After.Revision {
		return nil, &ConflictError{ID: current.ID}
	}
	child, err := c.readCreationChild(ctx, root)
	if err != nil {
		return nil, err
	}
	if child != nil && child.Phase != "prepared" && child.Phase != "applied" {
		return nil, fmt.Errorf("creation relationship child acquired an undeclared inverse")
	}
	if frozen := root.CreationCommand.Relations; frozen != nil && (child == nil || !sameCreationChildReceipt(frozen, child)) {
		return nil, fmt.Errorf("creation lost or changed its frozen relation result")
	}
	if root.Phase == "applied" && child != nil && root.CreationCommand.Relations == nil {
		return nil, fmt.Errorf("completed partial creation acquired a later child attempt")
	}
	result := &ActionReconcileResult{Issue: current, Journal: root, AlreadyReconciled: root.Phase == "applied", TargetWriteReplayed: false, ForwardOutcome: creationForwardOutcome(root)}
	result.Partial = child == nil && len(root.CreationCommand.Blocks) != 0
	// The parent's original receipt remains a publication fence for every
	// repaired child receipt, including multipart uploads.
	ctx = context.WithValue(ctx, creationCommandContextKey{}, &activeCreationCommand{journal: root, allocated: current})
	ctx = context.WithValue(ctx, creationChildContextKey{}, root.OperationID)
	if child == nil {
		if err := c.verifyCreationOriginal(ctx, root, current); err != nil {
			return nil, err
		}
	} else {
		observation, err := c.reconcileActionGroup(ctx, child, session, actor, apply && !result.AlreadyReconciled, false)
		if err != nil {
			return nil, err
		}
		result.GroupMembers, result.Applied = observation.GroupMembers, observation.Applied
		child = observation.Journal
		if child.Group.Forward != nil {
			result.Partial = child.Group.Forward.Partial
		} else {
			for _, member := range observation.GroupMembers {
				if member.IssueID != root.IssueID && (member.Status == "unattempted" || member.Status == "unapplied") {
					result.Partial = true
				}
			}
		}
	}
	if !apply || result.AlreadyReconciled {
		return result, nil
	}
	next, err := copyCreationJournal(root.ActionJournal)
	if err != nil {
		return nil, err
	}
	next.Phase = "applied"
	if next.After == nil {
		next.IssueID, next.After = current.ID, &state
		next.CreationCommand.Allocation = &CreationObservation{Proof: "observed", SessionID: session, Actor: actor, ObservedAt: time.Now().UTC()}
	}
	next.CreationCommand.Relations = child
	next.CreationCommand.Partial = child == nil && len(next.CreationCommand.Blocks) != 0 || child != nil && child.Group.Forward.Partial
	next.CreationCommand.Forward = &CreationObservation{Proof: "observed", SessionID: session, Actor: actor, ObservedAt: time.Now().UTC()}
	fence := func() error {
		actual, err := c.observeReconcileResult(ctx, root)
		if err != nil {
			return err
		}
		observed, err := actionIssueState(actual)
		if err != nil {
			return err
		}
		if observed.Revision != state.Revision {
			return &ConflictError{ID: actual.ID}
		}
		fresh, err := c.readCreationChild(ctx, root)
		if err != nil {
			return err
		}
		if !sameCreationChildReceipt(child, fresh) {
			return fmt.Errorf("creation relationship result changed before receipt write")
		}
		if child == nil {
			return c.verifyCreationOriginal(ctx, root, current)
		}
		_, err = c.observePairedGroup(ctx, child, false)
		return err
	}
	written, err := c.writeCreationReceipt(ctx, root, next, fence)
	if err != nil {
		return nil, err
	}
	// Re-read the allocated task after the parent receipt. Never reinterpret a
	// later native edit as part of the creation that was just observed.
	latest, err := c.GetIncludingDeleted(ctx, current.ID)
	if err != nil {
		return nil, err
	}
	latestState, err := actionIssueState(latest)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(latestState, state) {
		return nil, &ConflictError{ID: current.ID, AfterWrite: true}
	}
	result.Journal, result.Applied = written, true
	result.ForwardOutcome = creationForwardOutcome(written)
	return result, nil
}

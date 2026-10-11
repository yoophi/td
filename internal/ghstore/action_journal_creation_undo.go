package ghstore

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func creationInverseOutcome(root *ActionJournalRecord) *ActionUndoOutcome {
	verified := false
	if inverse := root.CreationCommand.Inverse; inverse != nil && inverse.Result != nil {
		verified = inverse.Result.Proof == "verified" && root.CreationCommand.Forward.Proof == "verified"
		if cp := inverse.Checkpoint; cp != nil && cp.Relations != nil {
			verified = verified && groupResultOutcome(cp.Relations, true).TargetWriteVerified
		}
	}
	proof := "creation-compensation-observed"
	if verified {
		proof = "verified-creation-compensation"
	}
	return &ActionUndoOutcome{Proof: proof, TargetWriteVerified: verified, CausalUndoChainEligible: false}
}

func creationInverseContext(ctx context.Context, root *ActionJournalRecord) context.Context {
	ctx = context.WithValue(ctx, creationCommandContextKey{}, &activeCreationCommand{journal: root})
	return context.WithValue(ctx, creationChildContextKey{}, root.OperationID)
}

func (c *Client) undoCreationCommand(ctx context.Context, root *ActionJournalRecord, session string) (*ActionUndoResult, error) {
	if root.SessionID != session {
		return nil, fmt.Errorf("creation undo requires its original session")
	}
	if root.Phase == "prepared" {
		return nil, fmt.Errorf("creation result is uncertain; reconcile %s --apply before undo", root.ID)
	}
	data, err := c.requestAPI(ctx, "GET", "user", "/user", nil, false)
	if err != nil {
		return nil, err
	}
	var actor struct{ Login string }
	if json.Unmarshal(data, &actor) != nil || actor.Login == "" || !strings.EqualFold(root.Author, actor.Login) {
		return nil, fmt.Errorf("creation undo requires its original authenticated GitHub author")
	}
	if root.Phase == "undone" {
		current, _, err := c.observeCreationInverse(ctx, root, true)
		if err != nil {
			return nil, err
		}
		return &ActionUndoResult{Issue: current, Journal: root, AlreadyUndone: true, UndoOutcome: creationInverseOutcome(root)}, nil
	}
	if inverse := root.CreationCommand.Inverse; inverse != nil && inverse.Checkpoint != nil {
		return nil, fmt.Errorf("creation deletion outcome is uncertain; use undo --reconcile %s --apply; no target replay attempted", root.ID)
	}
	current, child, err := c.observeCreationInverse(ctx, root, false)
	if err != nil {
		return nil, err
	}
	// Validate every remaining relationship inverse and the deletion input
	// before publishing the parent inverse intent or changing an existing task.
	if child != nil {
		observation, err := c.observePairedGroup(ctx, child, false)
		if err != nil {
			return nil, err
		}
		if observation.Pending != nil {
			return nil, fmt.Errorf("creation relationship inverse is uncertain; reconcile %s without replay", root.ID)
		}
		if err := c.preflightPairedInverse(ctx, child, observation, session, actor.Login); err != nil {
			return nil, err
		}
	}
	if root.Phase == "applied" {
		plan, err := c.prepareDeletion(ctx, current, true, session, "Undo related creation "+root.OperationID)
		if err != nil {
			return nil, err
		}
		input, err := updateInputFromPayload(*root.After, plan.payload)
		if err != nil {
			return nil, err
		}
		next, err := copyCreationJournal(root.ActionJournal)
		if err != nil {
			return nil, err
		}
		next.Phase, next.UndoOperationID = "undo_prepared", "td-op-"+rand.Text()
		next.CreationCommand.Inverse = &ActionCreationInverse{DeleteInput: input}
		written, err := c.writeCreationInverseReceipt(ctx, root, next, func() error {
			_, actual, err := c.observeCreationInverse(ctx, root, false)
			if err != nil {
				return err
			}
			if !sameCreationChildReceipt(child, actual) {
				return fmt.Errorf("creation relation receipt changed before inverse intent")
			}
			if child != nil {
				_, err = c.observePairedGroup(ctx, child, false)
			}
			return err
		})
		if err != nil {
			return nil, err
		}
		root = written
	}
	ctx = creationInverseContext(ctx, root)
	fence := func() error {
		_, _, err := c.observeCreationInverse(ctx, root, false)
		return err
	}
	ctx = context.WithValue(ctx, creationInverseFenceKey{}, fence)
	if child != nil && child.Phase != "undone" {
		result, err := c.UndoAction(ctx, child.ID, session)
		if err != nil {
			return nil, fmt.Errorf("creation %s relationship inverse stopped; earlier changes remain; reconcile without replay: %w", root.ID, err)
		}
		child = result.Journal
	}
	cp := &CreationInverseCheckpoint{Relations: child, Receipts: []*ActionJournalRecord{}}
	if child != nil {
		observed, err := c.observePairedGroup(ctx, child, false)
		if err != nil {
			return nil, err
		}
		cp.Receipts = observed.Receipts
		if cp.Receipts == nil {
			cp.Receipts = []*ActionJournalRecord{}
		}
	}
	next, err := copyCreationJournal(root.ActionJournal)
	if err != nil {
		return nil, err
	}
	next.CreationCommand.Inverse.Checkpoint = cp
	written, err := c.writeCreationInverseReceipt(ctx, root, next, func() error {
		if err := fence(); err != nil {
			return err
		}
		if child != nil {
			return c.fencePairedGroup(ctx, child, cp.Receipts, creationCheckpointStates(child), map[string]bool{})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	root = written
	ctx = creationInverseContext(ctx, root)
	fence = func() error {
		_, _, err := c.observeCreationInverse(ctx, root, false)
		return err
	}
	ctx = context.WithValue(ctx, creationInverseFenceKey{}, fence)
	current, _, err = c.observeCreationInverse(ctx, root, false)
	if err != nil {
		return nil, err
	}
	input := root.CreationCommand.Inverse.DeleteInput
	// The parent owns this complete frozen input. Publishing a second ordinary
	// delete action would split one command and allow an independent undo.
	untracked := *c
	untracked.journal = nil
	plan := &issueUpdatePlan{observed: current, payload: map[string]any{"title": input.Title, "body": input.Body, "state": input.State, "labels": input.Labels}, change: Changes{Labels: &input.Labels}}
	deleted, err := untracked.writeIssueUpdate(ctx, plan)
	if err != nil {
		return nil, fmt.Errorf("creation %s logical deletion is unverified; reconcile without repeating the target write: %w", root.ID, err)
	}
	// The post-write boundary expects the actual deletion, not the old source.
	ctx = context.WithValue(ctx, creationInverseFenceKey{}, (func() error)(nil))
	result, err := c.completeCreationInverse(ctx, root, deleted, session, actor.Login, true)
	if err != nil {
		return nil, err
	}
	return &ActionUndoResult{Issue: result.Issue, Journal: result.Journal, UndoOutcome: result.UndoOutcome}, nil
}

func creationCheckpointStates(child *ActionJournalRecord) map[string]ActionIssueState {
	out := map[string]ActionIssueState{}
	for _, m := range child.Group.Inverse.Members {
		out[m.IssueID] = m.State
	}
	return out
}

func (c *Client) completeCreationInverse(ctx context.Context, root *ActionJournalRecord, deleted *Record, session, actor string, verified bool) (*ActionReconcileResult, error) {
	state, err := actionIssueState(deleted)
	if err != nil {
		return nil, err
	}
	next, err := copyCreationJournal(root.ActionJournal)
	if err != nil {
		return nil, err
	}
	next.Phase, next.Restored = "undone", &state
	proof := "observed"
	if verified {
		proof = "verified"
	}
	next.CreationCommand.Inverse.Result = &CreationObservation{Proof: proof, SessionID: session, Actor: actor, ObservedAt: time.Now().UTC()}
	written, err := c.writeCreationInverseReceipt(ctx, root, next, func() error {
		actual, _, err := c.observeCreationInverse(ctx, root, true)
		if err != nil {
			return err
		}
		actualState, err := actionIssueState(actual)
		if err == nil && actualState.Revision != state.Revision {
			err = &ConflictError{ID: root.IssueID, AfterWrite: true}
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	latest, _, err := c.observeCreationInverse(ctx, written, true)
	if err != nil {
		return nil, err
	}
	return &ActionReconcileResult{Issue: latest, Journal: written, Applied: true, UndoOutcome: creationInverseOutcome(written)}, nil
}

func (c *Client) reconcileCreationInverse(ctx context.Context, root *ActionJournalRecord, session, actor string, apply bool) (*ActionReconcileResult, error) {
	ctx = creationInverseContext(ctx, root)
	if root.CreationCommand.Inverse.Checkpoint == nil {
		current, child, err := c.observeCreationInverse(ctx, root, false)
		if err != nil {
			return nil, err
		}
		result := &ActionReconcileResult{Issue: current, Journal: root, Partial: true, UndoOutcome: creationInverseOutcome(root)}
		if child != nil && child.Phase != "applied" {
			childResult, err := c.reconcileActionGroup(ctx, child, session, actor, apply, false)
			if err != nil {
				return nil, err
			}
			result.Applied, result.GroupMembers = childResult.Applied, childResult.GroupMembers
		}
		// No deletion intent checkpoint exists yet. Only an explicit undo may
		// continue unattempted target writes after the child receipts settle.
		return result, nil
	}
	current, _, err := c.observeCreationInverse(ctx, root, true)
	if err != nil {
		return nil, fmt.Errorf("creation deletion is unverified; target was not replayed: %w", err)
	}
	if root.Phase == "undone" || !apply {
		return &ActionReconcileResult{Issue: current, Journal: root, AlreadyReconciled: root.Phase == "undone", UndoOutcome: creationInverseOutcome(root)}, nil
	}
	return c.completeCreationInverse(ctx, root, current, session, actor, false)
}

package ghstore

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"
)

// The original native comment remains in the immutable journal even when its
// creation is undone. Generic comment deletion still protects audit activities.
func (c *Client) undoCommentAction(ctx context.Context, action *ActionJournalRecord, current *Record, sessionID, author string) (*ActionUndoResult, error) {
	plan, witnesses, err := c.planUndo(ctx, action, current, sessionID, author)
	if err != nil {
		return nil, err
	}
	state, err := actionIssueState(current)
	if err != nil {
		return nil, err
	}
	if plan.AlreadyUndone {
		if err := c.verifyDeletedComment(ctx, action); err != nil {
			return nil, err
		}
		latest, err := c.GetIncludingDeleted(ctx, current.ID)
		if err != nil {
			return nil, err
		}
		if latest.revision != current.revision {
			return nil, &ConflictError{ID: current.ID}
		}
		return &ActionUndoResult{Issue: current, Journal: action, AlreadyUndone: true, UndoOutcome: observedForwardInverseOutcome(action)}, nil
	}
	if !strings.EqualFold(action.CommentAfter.Author, author) {
		return nil, fmt.Errorf("native comment is not attributed to the current GitHub author")
	}
	if err := c.readNativeComment(ctx, action.CommentAfter); err != nil {
		return nil, err
	}
	pending := action.ActionJournal
	pending.Phase, pending.UndoOperationID = "undo_prepared", "td-op-"+rand.Text()
	prepared, err := c.rewriteActionJournalGuarded(ctx, action, pending, func() error { return checkGroupCommentUndoGate(ctx, action, current, false) })
	if err != nil {
		return nil, fmt.Errorf("undo intent is unverified; no comment DELETE attempted: %w", err)
	}
	if err := c.recheckUndoWitnesses(ctx, witnesses); err != nil {
		return nil, err
	}
	// Recheck both mutable objects after persisting intent and before DELETE.
	if err := c.readNativeComment(ctx, action.CommentAfter); err != nil {
		return nil, err
	}
	latest, err := c.GetIncludingDeleted(ctx, current.ID)
	if err != nil {
		return nil, err
	}
	if latest.revision != current.revision {
		return nil, &ConflictError{ID: current.ID}
	}
	if err := checkGroupCommentUndoGate(ctx, prepared, current, false); err != nil {
		return nil, err
	}
	uncertain := func(err error) (*ActionUndoResult, error) {
		return nil, fmt.Errorf("comment undo %s is unverified; inspect target %s and journal %s before retrying: %w", pending.UndoOperationID, current.ID, action.ID, err)
	}
	number, err := CommentNumber(action.CommentAfter.ID)
	if err != nil {
		return nil, err
	}
	if _, err := c.request(ctx, "DELETE", fmt.Sprintf("/issues/comments/%d", number), nil, false); err != nil {
		return uncertain(err)
	}
	if err := c.verifyDeletedComment(ctx, action); err != nil {
		return uncertain(err)
	}
	restored, err := c.GetIncludingDeleted(withReadbackCost(ctx), current.ID)
	if err != nil {
		return uncertain(err)
	}
	restoredState, err := actionIssueState(restored)
	if err != nil {
		return uncertain(err)
	}
	if !sameActionContents(*action.Before, restoredState) || restoredState.UpdatedAt.Before(state.UpdatedAt) {
		return uncertain(&ConflictError{ID: current.ID, AfterWrite: true})
	}
	done := prepared.ActionJournal
	done.Phase, done.Restored = "undone", &restoredState
	receipt, err := c.rewriteActionJournalGuarded(ctx, prepared, done, func() error { return checkGroupCommentUndoGate(ctx, prepared, restored, true) })
	if err != nil {
		return uncertain(err)
	}
	if err := c.verifyDeletedComment(ctx, receipt); err != nil {
		return uncertain(err)
	}
	latest, err = c.GetIncludingDeleted(withReadbackCost(ctx), current.ID)
	if err != nil {
		return uncertain(err)
	}
	if latest.revision != restored.revision {
		return uncertain(&ConflictError{ID: current.ID, AfterWrite: true})
	}
	if err := checkGroupCommentUndoGate(ctx, receipt, restored, true); err != nil {
		return uncertain(err)
	}
	return &ActionUndoResult{Issue: restored, Journal: receipt, UndoOutcome: observedForwardInverseOutcome(receipt)}, nil
}

func (c *Client) verifyDeletedComment(ctx context.Context, action *ActionJournalRecord) error {
	snapshot, err := c.ReadSnapshot(WithFreshSnapshot(ctx), true)
	if err != nil {
		return err
	}
	if _, err := snapshot.ActionHistory(""); err != nil {
		return err
	}
	return deletedCommentInSnapshot(snapshot, action)
}

func deletedCommentInSnapshot(snapshot *Snapshot, action *ActionJournalRecord) error {
	number, err := CommentNumber(action.CommentAfter.ID)
	if err != nil {
		return err
	}
	operationID := action.CommentInput.OperationID
	if operationID == "" {
		operationID = action.OperationID
	}
	foundTarget := false
	for _, entry := range snapshot.entries {
		if fmt.Sprintf("gh-%d", entry.Issue.Number) == action.IssueID {
			foundTarget = true
		}
		for _, comment := range entry.Comments {
			if comment.ID == number {
				return fmt.Errorf("native comment %s is still present; deletion is unverified", action.CommentAfter.ID)
			}
			if fmt.Sprintf("gh-%d", entry.Issue.Number) != action.IssueID {
				continue
			}
			activity, err := decodeActivity(comment, action.IssueID)
			if err != nil {
				return err
			}
			if activity.Kind != "action_journal" && activity.OperationID == operationID {
				return fmt.Errorf("activity operation %s is still present on %s; deletion is unverified", operationID, action.IssueID)
			}
		}
	}
	if !foundTarget {
		return fmt.Errorf("target %s is missing from the fresh collection; comment deletion is unverified", action.IssueID)
	}
	return nil
}

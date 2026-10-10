package ghstore

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
)

// Only this command's observed, deleted companion comment can bridge the
// update's clock. It never becomes a general causal witness for other actions.
func updateActivityUndoPlan(action *ActionJournalRecord, current *Record, session, author string, receipts []*ActionJournalRecord) (*IssueUndoPlan, error) {
	if action.Action == "comment_create" || action.Phase != "applied" {
		return action.planActionUndo(current, session, author)
	}
	state, err := actionIssueState(current)
	if err != nil {
		return nil, err
	}
	if state.Revision == action.After.Revision {
		return action.PlanIssueUndo(current, session, author)
	}
	if len(receipts) != 2 || receipts[0].ID != action.ID {
		return nil, &ConflictError{ID: action.IssueID}
	}
	comment := receipts[1]
	if comment.Phase != "undone" || comment.Restored == nil || comment.Before.Revision != action.After.Revision || comment.Restored.Revision != state.Revision || !sameActionContents(*action.After, state) {
		return nil, &ConflictError{ID: action.IssueID}
	}
	probe := *action
	after := *action.After
	after.UpdatedAt, after.Revision = state.UpdatedAt, state.Revision
	probe.After = &after
	return probe.PlanIssueUndo(current, session, author)
}

func (c *Client) undoUpdateActivity(ctx context.Context, root *ActionJournalRecord, session string) (*ActionUndoResult, error) {
	if root.SessionID != session {
		return nil, fmt.Errorf("update/activity undo requires its original session")
	}
	if root.Phase == "prepared" {
		return nil, fmt.Errorf("update/activity outcome is unverified; use undo --reconcile %s --apply before undo", root.ID)
	}
	profile, err := c.requestAPI(ctx, "GET", "user", "/user", nil, false)
	if err != nil {
		return nil, err
	}
	var user struct{ Login string }
	if json.Unmarshal(profile, &user) != nil || user.Login == "" || !strings.EqualFold(user.Login, root.Author) {
		return nil, fmt.Errorf("update/activity undo requires the original authenticated GitHub author")
	}
	observed, err := c.observeUpdateActivity(ctx, root, false)
	if err != nil {
		return nil, err
	}
	if observed.Pending != nil {
		return nil, fmt.Errorf("update/activity inverse is uncertain; reconcile %s without target replay before continuing", root.ID)
	}
	if root.Phase == "undone" {
		return &ActionUndoResult{Issue: observed.Current, Journal: root, AlreadyUndone: true, GroupMembers: observed.Members, UndoOutcome: groupResultOutcome(root, true)}, nil
	}
	// Validate the complete field inverse before deleting its companion activity.
	for _, receipt := range observed.Receipts {
		if receipt.Phase != "applied" {
			continue
		}
		if receipt.Action == "comment_create" {
			if _, err := receipt.planActionUndo(observed.Current, session, user.Login); err != nil {
				return nil, err
			}
			if err := c.readNativeComment(ctx, receipt.CommentAfter); err != nil {
				return nil, err
			}
			continue
		}
		if err := scalarUndoScope(receipt); err != nil {
			return nil, err
		}
		if !sameActionContents(*receipt.After, observed.State) {
			return nil, &ConflictError{ID: root.IssueID}
		}
		if err := c.checkUndoOriginalReview(ctx, receipt, false); err != nil {
			return nil, err
		}
	}
	if root.Phase == "applied" {
		next := root.ActionJournal
		next.Phase, next.UndoOperationID = "undo_prepared", "td-op-"+rand.Text()
		written, err := c.writeGroupReceipt(ctx, root, next, func() error {
			return c.fenceUpdateActivity(ctx, root, observed.Receipts, observed.State, observed.Comment != nil)
		})
		if err != nil {
			return nil, err
		}
		root = written
	}
	for i := len(observed.Receipts) - 1; i >= 0; i-- {
		member := observed.Receipts[i]
		if member.Phase == "prepared" || member.Phase == "undone" {
			continue
		}
		if err := c.fenceUpdateActivity(ctx, root, observed.Receipts, observed.State, observed.Comment != nil); err != nil {
			return nil, err
		}
		fenceMember := func(actual *ActionJournalRecord, current *Record, present bool) error {
			want := slices.Clone(observed.Receipts)
			want[i] = actual
			state, err := actionIssueState(current)
			if err != nil {
				return err
			}
			return c.fenceUpdateActivity(ctx, root, want, state, present)
		}
		gate := &groupUndoGate{OperationID: root.OperationID,
			Member: func(actual *ActionJournalRecord) error {
				if !reflect.DeepEqual(member, actual) {
					return fmt.Errorf("update/activity member changed before inverse")
				}
				return nil
			},
			Plan: func(actual *ActionJournalRecord, current *Record, session, author string) (*IssueUndoPlan, error) {
				return updateActivityUndoPlan(actual, current, session, author, observed.Receipts)
			},
			CommentFence: func(actual *ActionJournalRecord, current *Record, deleted bool) error {
				return fenceMember(actual, current, !deleted)
			},
			UpdateFence: func(actual *ActionJournalRecord, current *Record) error { return fenceMember(actual, current, false) },
		}
		gated := context.WithValue(ctx, groupUndoContextKey{}, gate)
		if _, err := c.UndoAction(gated, member.ID, session); err != nil {
			return nil, fmt.Errorf("update/activity group %s inverse stopped at %s; inspect and reconcile without replay: %w", root.ID, member.ID, err)
		}
		observed, err = c.observeUpdateActivity(ctx, root, false)
		if err != nil {
			return nil, err
		}
	}
	result, err := c.reconcileUpdateActivity(ctx, root, session, user.Login, true, true)
	if err != nil {
		return nil, err
	}
	return &ActionUndoResult{Issue: result.Issue, Journal: result.Journal, GroupMembers: result.GroupMembers, UndoOutcome: groupResultOutcome(result.Journal, true)}, nil
}

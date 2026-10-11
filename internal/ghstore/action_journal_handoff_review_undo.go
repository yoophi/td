package ghstore

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
)

// Only this group's actual review inverse may bridge its own handoff clock.
// It is not evidence for undoing unrelated earlier commands.
func handoffReviewUndoPlan(action *ActionJournalRecord, current *Record, session, author string, receipts []*ActionJournalRecord) (*IssueUndoPlan, error) {
	if action.Action != "comment_create" || action.Phase != "applied" {
		return action.planActionUndo(current, session, author)
	}
	state, err := actionIssueState(current)
	if err != nil {
		return nil, err
	}
	if state.Revision == action.After.Revision {
		return action.planActionUndo(current, session, author)
	}
	for _, r := range receipts {
		if r.IssueID != action.IssueID || r.Action != "update" || r.GroupPreviousReceiptID != action.ID {
			continue
		}
		if r.Phase != "undone" || r.Restored == nil || r.Before.Revision != action.After.Revision || r.Restored.Revision != state.Revision || !sameActionContents(*action.After, state) {
			return nil, &ConflictError{ID: action.IssueID}
		}
		probe := *action
		after := *action.After
		after.UpdatedAt, after.Revision = state.UpdatedAt, state.Revision
		probe.After = &after
		return probe.planActionUndo(current, session, author)
	}
	return nil, &ConflictError{ID: action.IssueID}
}

func (c *Client) undoPairedGroup(ctx context.Context, root *ActionJournalRecord, session string) (*ActionUndoResult, error) {
	if root.SessionID != session {
		return nil, fmt.Errorf("paired command undo requires its original session")
	}
	if root.Phase == "prepared" {
		return nil, fmt.Errorf("paired command outcome is unverified; use undo --reconcile %s --apply before undo", root.ID)
	}
	data, err := c.requestAPI(ctx, "GET", "user", "/user", nil, false)
	if err != nil {
		return nil, err
	}
	var user struct{ Login string }
	if json.Unmarshal(data, &user) != nil || user.Login == "" || !strings.EqualFold(root.Author, user.Login) {
		return nil, fmt.Errorf("paired command undo requires the original authenticated GitHub author")
	}
	observed, err := c.observePairedGroup(ctx, root, false)
	if err != nil {
		return nil, err
	}
	if observed.Pending != nil {
		return nil, fmt.Errorf("paired command inverse is uncertain; reconcile %s without target replay before continuing", root.ID)
	}
	if root.Phase == "undone" {
		return &ActionUndoResult{Issue: observed.Current[root.IssueID], Journal: root, AlreadyUndone: true, GroupMembers: observed.Members, UndoOutcome: groupResultOutcome(root, true)}, nil
	}
	// Check every remaining inverse before the first mutation. Exact comment
	// clocks are checked again after any companion effect has been restored.
	for _, r := range observed.Receipts {
		if r.Phase != "applied" {
			continue
		}
		if r.Action == "comment_create" {
			if !strings.EqualFold(r.CommentAfter.Author, user.Login) {
				return nil, fmt.Errorf("paired command comment author changed")
			}
			if err := c.readNativeComment(ctx, r.CommentAfter); err != nil {
				return nil, err
			}
		} else {
			if root.Group.fieldCommentGroup() {
				if err := scalarUndoScope(r); err != nil {
					return nil, err
				}
				if !sameActionContents(*r.After, observed.States[r.IssueID]) {
					return nil, &ConflictError{ID: r.IssueID}
				}
			} else if _, err := r.PlanIssueUndo(observed.Current[r.IssueID], session, user.Login); err != nil {
				return nil, err
			}
			if err := c.checkUndoOriginalReview(ctx, r, false); err != nil {
				return nil, err
			}
		}
	}
	if err := c.preflightRelationGroupInverse(ctx, root, observed); err != nil {
		return nil, err
	}
	if root.Phase == "applied" {
		next := root.ActionJournal
		next.Phase, next.UndoOperationID = "undo_prepared", "td-op-"+rand.Text()
		written, err := c.writeGroupReceipt(ctx, root, next, func() error {
			return c.fencePairedGroup(ctx, root, observed.Receipts, observed.States, observed.present())
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
		if err := c.fencePairedGroup(ctx, root, observed.Receipts, observed.States, observed.present()); err != nil {
			return nil, err
		}
		fenceMember := func(actual *ActionJournalRecord, current *Record, deleted bool) error {
			want := slices.Clone(observed.Receipts)
			want[i] = actual
			states := maps.Clone(observed.States)
			state, err := actionIssueState(current)
			if err != nil {
				return err
			}
			states[current.ID] = state
			present := observed.present()
			if actual.Action == "comment_create" {
				present[current.ID] = !deleted
			}
			return c.fencePairedGroup(ctx, root, want, states, present)
		}
		gate := &groupUndoGate{OperationID: root.OperationID,
			Member: func(actual *ActionJournalRecord) error {
				if !reflect.DeepEqual(member, actual) {
					return fmt.Errorf("paired command member changed before inverse")
				}
				return nil
			},
			Plan: func(actual *ActionJournalRecord, current *Record, session, author string) (*IssueUndoPlan, error) {
				if root.Group.fieldCommentGroup() {
					chain := []*ActionJournalRecord{}
					for _, receipt := range observed.Receipts {
						if receipt.IssueID == actual.IssueID {
							chain = append(chain, receipt)
						}
					}
					return updateActivityUndoPlan(actual, current, session, author, chain)
				}
				return handoffReviewUndoPlan(actual, current, session, author, observed.Receipts)
			},
			CommentFence: fenceMember,
			UpdateFence:  func(actual *ActionJournalRecord, current *Record) error { return fenceMember(actual, current, false) },
		}
		if _, err := c.UndoAction(context.WithValue(ctx, groupUndoContextKey{}, gate), member.ID, session); err != nil {
			return nil, fmt.Errorf("paired command group %s inverse stopped at %s; inspect and reconcile without replay: %w", root.ID, member.ID, err)
		}
		observed, err = c.observePairedGroup(ctx, root, false)
		if err != nil {
			return nil, err
		}
	}
	result, err := c.reconcilePairedGroup(ctx, root, session, user.Login, true, true)
	if err != nil {
		return nil, err
	}
	return &ActionUndoResult{Issue: result.Issue, Journal: result.Journal, GroupMembers: result.GroupMembers, UndoOutcome: groupResultOutcome(result.Journal, true)}, nil
}

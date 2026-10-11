package ghstore

import (
	"context"
	"fmt"
	"reflect"
	"slices"
)

type updateActivityObservation struct {
	Snapshot  *Snapshot
	History   []ActionJournalRecord
	Bridges   []ActionJournalRecord
	Current   *Record
	State     ActionIssueState
	Receipts  []*ActionJournalRecord
	Comment   *ActionCommentState
	Members   []ActionGroupResultMember
	Pending   *ActionJournalRecord
	Partial   bool
	Remaining bool
	Weak      bool
}

func (c *Client) readUpdateActivity(ctx context.Context, root *ActionJournalRecord) (*updateActivityObservation, error) {
	snapshot, err := c.ReadSnapshot(WithFreshSnapshot(ctx), true)
	if err != nil {
		return nil, err
	}
	history, err := snapshot.ActionHistory("")
	if err != nil {
		return nil, err
	}
	out := &updateActivityObservation{Snapshot: snapshot, History: history}
	carriers, found := 0, false
	for i := range snapshot.records {
		r := &snapshot.records[i]
		if r.meta.EntityKind == "journal" {
			carriers++
			if r.ID != root.CarrierID {
				return nil, fmt.Errorf("update/activity carrier changed")
			}
		}
		if r.ID == root.IssueID {
			out.Current = r
		}
	}
	for i := range history {
		r := &history[i]
		if r.ID == root.ID {
			if found || !reflect.DeepEqual(r, root) {
				return nil, fmt.Errorf("update/activity root changed")
			}
			found = true
		}
		if r.GroupOperationID == root.OperationID {
			out.Receipts = append(out.Receipts, r)
		}
	}
	if !found || carriers != 1 || out.Current == nil {
		return nil, fmt.Errorf("update/activity command needs its original carrier, task and root receipt")
	}
	chains, err := orderedGroupEffects(root, out.Receipts)
	if err != nil {
		return nil, err
	}
	out.Receipts = chains[root.IssueID]
	if err := validateUpdateActivityForward(root, out.Receipts); err != nil {
		return nil, err
	}
	out.State, err = actionIssueState(out.Current)
	if err != nil {
		return nil, err
	}
	probe := *root
	probe.CommentInput = root.Group.UpdateActivity.Comment
	if len(out.Receipts) == 2 {
		probe = *out.Receipts[1]
		probe.CommentAfter = nil
	}
	out.Comment, err = c.commentCreationInSnapshot(snapshot, &probe, out.State)
	if err != nil {
		return nil, err
	}
	if len(out.Receipts) < 2 && out.Comment != nil {
		return nil, fmt.Errorf("unattempted update/activity command acquired a comment")
	}
	if len(out.Receipts) == 2 {
		comment := out.Receipts[1]
		if comment.Phase == "applied" && !reflect.DeepEqual(comment.CommentAfter, out.Comment) {
			return nil, fmt.Errorf("update/activity native comment changed")
		}
		if comment.Phase == "undo_prepared" && out.Comment != nil && !reflect.DeepEqual(comment.CommentAfter, out.Comment) {
			return nil, fmt.Errorf("update/activity inverse comment changed")
		}
		if comment.Phase == "undone" || comment.Phase == "undo_prepared" && out.Comment == nil {
			if err := deletedCommentInSnapshot(snapshot, comment); err != nil {
				return nil, err
			}
		}
	}
	if err := c.verifyUpdateActivityEvents(ctx, root); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) fenceUpdateActivity(ctx context.Context, root *ActionJournalRecord, receipts []*ActionJournalRecord, state ActionIssueState, present bool, bridges ...[]ActionJournalRecord) error {
	current, err := c.readUpdateActivity(ctx, root)
	if err != nil {
		return err
	}
	for _, witnesses := range bridges {
		if err := c.checkUndoWitnessesSnapshot(ctx, current.Snapshot, witnesses); err != nil {
			return err
		}
	}
	if current.State.Revision != state.Revision || !slices.EqualFunc(current.Receipts, receipts, func(a, b *ActionJournalRecord) bool { return reflect.DeepEqual(a, b) }) || (current.Comment != nil) != present {
		return fmt.Errorf("update/activity task, native comment or effect receipts changed: %w", &ConflictError{ID: root.IssueID})
	}
	return nil
}

func (c *Client) observeUpdateActivity(ctx context.Context, root *ActionJournalRecord, normal bool) (*updateActivityObservation, error) {
	out, err := c.readUpdateActivity(ctx, root)
	if err != nil {
		return nil, err
	}
	inverse := root.Phase == "undo_prepared" || root.Phase == "undone"
	expected := root.Group.Members[0].Before
	result := ActionGroupResultMember{IssueID: root.IssueID, Status: "unattempted", State: expected}
	applied := []*ActionJournalRecord{}
	for _, r := range out.Receipts {
		out.Weak = out.Weak || r.UpdateObservation != nil || r.CommentObservation != nil || r.UndoObservation != nil
		if r.Phase == "prepared" {
			out.Partial = true
			if inverse {
				if out.Comment != nil {
					return nil, fmt.Errorf("unapplied update/activity effect acquired a comment during undo")
				}
				if len(applied) == 0 {
					result.Status, result.ReceiptID = "unapplied", r.ID
				}
				continue
			}
			if out.State.Revision == r.Before.Revision && out.Comment == nil {
				if len(applied) == 0 {
					result.Status, result.ReceiptID = "unapplied", r.ID
				}
				continue
			}
			want := *r.Before
			if r.UpdateIntent != nil {
				want, err = r.UpdateIntent.Target.contents()
				if err != nil {
					return nil, err
				}
			}
			if inverse || !sameActionContents(want, out.State) || out.State.UpdatedAt.Before(r.Before.UpdatedAt) || r.Action == "comment_create" && out.Comment == nil {
				return nil, &ConflictError{ID: root.IssueID}
			}
			out.Pending = r
			expected = out.State
			result.Status, result.ReceiptID = "pending-applied", r.ID
			continue
		}
		if !inverse && r.Phase != "applied" {
			return nil, fmt.Errorf("update/activity effect acquired an inverse before its group")
		}
		applied = append(applied, r)
		expected = *r.After
		result.Status, result.ReceiptID = "applied", r.ID
	}
	out.Partial = out.Partial || len(out.Receipts) < 2
	forward := result
	forward.State = expected
	if inverse {
		if root.Group.Forward.Partial != out.Partial || !equalGroupMemberResults(root.Group.Forward.Members, []ActionGroupResultMember{forward}) {
			// A recovered forward result keeps its weaker status and attribution.
			forward.Status = "observed"
			if root.Group.Forward.Partial != out.Partial || !equalGroupMemberResults(root.Group.Forward.Members, []ActionGroupResultMember{forward}) {
				return nil, fmt.Errorf("update/activity original forward result changed")
			}
		}
		stopped := false
		for i := len(applied) - 1; i >= 0; i-- {
			r := applied[i]
			switch r.Phase {
			case "undone":
				if stopped || r.Restored == nil || !sameActionContents(*r.Before, *r.Restored) || r.Restored.UpdatedAt.Before(expected.UpdatedAt) {
					return nil, fmt.Errorf("update/activity inverse order or original contents changed")
				}
				expected = *r.Restored
			case "undo_prepared":
				if stopped || out.Pending != nil || !sameActionContents(*r.Before, out.State) || out.State.UpdatedAt.Before(expected.UpdatedAt) || r.Action == "comment_create" && out.Comment != nil {
					return nil, fmt.Errorf("update/activity inverse is unverified; inspect without replaying it")
				}
				out.Pending, expected, stopped = r, out.State, true
			case "applied":
				out.Remaining, stopped = true, true
			}
		}
		if len(applied) > 0 && !out.Remaining {
			result.Status = "undone"
			if out.Pending != nil {
				result.Status = "pending-undone"
			}
		}
	}
	if out.State.Revision != expected.Revision {
		var terminal *ActionJournalRecord
		if !normal && root.Phase != "prepared" && root.Phase != "undone" {
			for _, receipt := range out.Receipts {
				if receipt.Phase == "applied" && receipt.After.Revision == expected.Revision {
					terminal = receipt
				}
			}
		}
		if terminal == nil {
			return nil, &ConflictError{ID: root.IssueID}
		}
		_, witnesses, err := planUndoThroughReceipts(terminal, out.Current, root.SessionID, root.Author, out.History)
		if err != nil {
			return nil, err
		}
		if err := c.checkUndoWitnessesSnapshot(ctx, out.Snapshot, witnesses); err != nil {
			return nil, err
		}
		out.Bridges = witnesses
	}
	if normal && (out.Pending != nil || !inverse && out.Partial) {
		return nil, fmt.Errorf("successful update/activity callback omitted or failed to verify an effect")
	}
	result.State = out.State
	out.Members = []ActionGroupResultMember{result}
	saved := root.Group.Forward
	if inverse {
		saved = root.Group.Inverse
	}
	// Compare saved clocks without rewriting original receipts or the actual
	// current state used for the inverse and each fresh write fence.
	comparison := result
	if len(out.Bridges) > 0 {
		comparison.State = expected
	}
	if saved != nil && !equalGroupMemberResults(saved.Members, []ActionGroupResultMember{comparison}) {
		return nil, fmt.Errorf("update/activity completed result changed")
	}
	return out, nil
}

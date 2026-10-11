package ghstore

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"slices"
)

type pairedGroupObservation struct {
	Current                  map[string]*Record
	States                   map[string]ActionIssueState
	Comments                 map[string]*ActionCommentState
	Receipts                 []*ActionJournalRecord
	Members                  []ActionGroupResultMember
	Pending                  *ActionJournalRecord
	Partial, Remaining, Weak bool
}

func (o *pairedGroupObservation) present() map[string]bool {
	out := map[string]bool{}
	for id, c := range o.Comments {
		out[id] = c != nil
	}
	return out
}
func (c *Client) readPairedGroup(ctx context.Context, root *ActionJournalRecord) (*pairedGroupObservation, error) {
	snapshot, err := c.ReadSnapshot(WithFreshSnapshot(ctx), true)
	if err != nil {
		return nil, err
	}
	history, err := snapshot.ActionHistory("")
	if err != nil {
		return nil, err
	}
	out := &pairedGroupObservation{Current: map[string]*Record{}, States: map[string]ActionIssueState{}, Comments: map[string]*ActionCommentState{}}
	for _, m := range root.Group.Members {
		out.Current[m.IssueID] = nil
	}
	carriers, found := 0, false
	for i := range snapshot.records {
		r := &snapshot.records[i]
		if r.meta.EntityKind == "journal" {
			carriers++
			if r.ID != root.CarrierID {
				return nil, fmt.Errorf("paired command carrier changed")
			}
		}
		if _, ok := out.Current[r.ID]; ok {
			out.Current[r.ID] = r
			state, err := actionIssueState(r)
			if err != nil {
				return nil, err
			}
			out.States[r.ID] = state
		}
	}
	for i := range history {
		r := &history[i]
		if r.ID == root.ID {
			if found || !reflect.DeepEqual(r, root) {
				return nil, fmt.Errorf("paired command root changed")
			}
			found = true
		}
		if r.GroupOperationID == root.OperationID {
			out.Receipts = append(out.Receipts, r)
		}
	}
	if !found || carriers != 1 || len(out.States) != len(out.Current) {
		return nil, fmt.Errorf("paired command needs its original carrier, tasks and root")
	}
	slices.SortFunc(out.Receipts, func(a, b *ActionJournalRecord) int { return a.GroupSequence - b.GroupSequence })
	chains, err := orderedGroupEffects(root, out.Receipts)
	if err != nil {
		return nil, err
	}
	if err := validatePairedGroupForward(root, out.Receipts); err != nil {
		return nil, err
	}
	for i, m := range root.Group.Members {
		probe := *root
		probe.IssueID = m.IssueID
		probe.CommentInput = root.Group.pairedComment(i)
		chain := chains[m.IssueID]
		var receipt *ActionJournalRecord
		for _, effect := range chain {
			if effect.Action == "comment_create" {
				receipt = effect
				probe = *effect
				probe.CommentAfter = nil
			}
		}
		comment, err := c.commentCreationInSnapshot(snapshot, &probe, out.States[m.IssueID])
		if err != nil {
			return nil, err
		}
		out.Comments[m.IssueID] = comment
		if receipt == nil {
			if comment != nil {
				return nil, fmt.Errorf("unattempted paired command acquired a native comment")
			}
			continue
		}
		if receipt.Phase == "applied" && !reflect.DeepEqual(receipt.CommentAfter, comment) || receipt.Phase == "undo_prepared" && comment != nil && !reflect.DeepEqual(receipt.CommentAfter, comment) {
			return nil, fmt.Errorf("paired command native comment changed")
		}
		if receipt.Phase == "undone" || receipt.Phase == "undo_prepared" && comment == nil {
			if err := deletedCommentInSnapshot(snapshot, receipt); err != nil {
				return nil, err
			}
		}
	}
	if err := c.verifyPairedGroupEvents(ctx, root); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) fencePairedGroup(ctx context.Context, root *ActionJournalRecord, receipts []*ActionJournalRecord, states map[string]ActionIssueState, present map[string]bool) error {
	current, err := c.readPairedGroup(ctx, root)
	if err != nil {
		return err
	}
	if !slices.EqualFunc(current.Receipts, receipts, func(a, b *ActionJournalRecord) bool { return reflect.DeepEqual(a, b) }) {
		return fmt.Errorf("paired command effect receipts changed")
	}
	for id, state := range states {
		if current.States[id].Revision != state.Revision || (current.Comments[id] != nil) != present[id] {
			return fmt.Errorf("paired command task or comment changed: %w", &ConflictError{ID: id})
		}
	}
	return nil
}

func (c *Client) observePairedGroup(ctx context.Context, root *ActionJournalRecord, normal bool) (*pairedGroupObservation, error) {
	out, err := c.readPairedGroup(ctx, root)
	if err != nil {
		return nil, err
	}
	inverse := root.Phase == "undo_prepared" || root.Phase == "undone"
	expected := map[string]ActionIssueState{}
	results := map[string]ActionGroupResultMember{}
	for _, m := range root.Group.Members {
		expected[m.IssueID] = m.Before
		results[m.IssueID] = ActionGroupResultMember{IssueID: m.IssueID, Status: "unattempted", State: m.Before}
	}
	applied := []*ActionJournalRecord{}
	out.Partial = len(out.Receipts) < root.Group.pairedEffectCount()
	for _, r := range out.Receipts {
		out.Weak = out.Weak || r.UpdateObservation != nil || r.CommentObservation != nil || r.UndoObservation != nil
		result := results[r.IssueID]
		if r.Phase == "prepared" {
			out.Partial = true
			if inverse {
				if r.Action == "comment_create" && out.Comments[r.IssueID] != nil {
					return nil, fmt.Errorf("unapplied paired command acquired a comment during undo")
				}
				if result.Status == "unattempted" {
					result.Status, result.ReceiptID = "unapplied", r.ID
				}
				results[r.IssueID] = result
				continue
			}
			state := out.States[r.IssueID]
			unapplied := state.Revision == r.Before.Revision && (r.Action != "comment_create" || out.Comments[r.IssueID] == nil)
			if unapplied {
				if result.Status == "unattempted" {
					result.Status, result.ReceiptID = "unapplied", r.ID
				}
				results[r.IssueID] = result
				continue
			}
			want := *r.Before
			if r.UpdateIntent != nil {
				want, err = r.UpdateIntent.Target.contents()
				if err != nil {
					return nil, err
				}
			}
			if !sameActionContents(want, state) || state.UpdatedAt.Before(r.Before.UpdatedAt) || r.Action == "comment_create" && out.Comments[r.IssueID] == nil {
				return nil, &ConflictError{ID: r.IssueID}
			}
			out.Pending = r
			expected[r.IssueID] = state
			result.Status, result.ReceiptID = "pending-applied", r.ID
			results[r.IssueID] = result
			continue
		}
		if !inverse && r.Phase != "applied" {
			return nil, fmt.Errorf("paired command effect acquired an inverse before its group")
		}
		applied = append(applied, r)
		expected[r.IssueID] = *r.After
		result.Status, result.ReceiptID = "applied", r.ID
		results[r.IssueID] = result
	}
	forward := make([]ActionGroupResultMember, 0, len(root.Group.Members))
	for _, m := range root.Group.Members {
		result := results[m.IssueID]
		result.State = expected[m.IssueID]
		forward = append(forward, result)
	}
	if inverse {
		if root.Group.Forward.Partial != out.Partial || !equalGroupMemberResults(root.Group.Forward.Members, forward) {
			return nil, fmt.Errorf("paired command original forward result changed")
		}
		stopped := false
		remaining := map[string]bool{}
		for i := len(applied) - 1; i >= 0; i-- {
			r := applied[i]
			state := out.States[r.IssueID]
			switch r.Phase {
			case "undone":
				if stopped || r.Restored == nil || !sameActionContents(*r.Before, *r.Restored) || r.Restored.UpdatedAt.Before(expected[r.IssueID].UpdatedAt) {
					return nil, fmt.Errorf("paired command inverse order or contents changed")
				}
				expected[r.IssueID] = *r.Restored
			case "undo_prepared":
				if stopped || out.Pending != nil || !sameActionContents(*r.Before, state) || state.UpdatedAt.Before(expected[r.IssueID].UpdatedAt) || r.Action == "comment_create" && out.Comments[r.IssueID] != nil {
					return nil, fmt.Errorf("paired command inverse is unverified; inspect without replay")
				}
				out.Pending, expected[r.IssueID], stopped = r, state, true
			case "applied":
				out.Remaining, stopped, remaining[r.IssueID] = true, true, true
			}
		}
		for id, result := range results {
			if result.Status == "applied" && !remaining[id] {
				result.Status = "undone"
				if out.Pending != nil && out.Pending.IssueID == id {
					result.Status = "pending-undone"
				}
				results[id] = result
			}
		}
	}
	for _, m := range root.Group.Members {
		if expected[m.IssueID].Revision != out.States[m.IssueID].Revision {
			return nil, &ConflictError{ID: m.IssueID}
		}
		result := results[m.IssueID]
		result.State = out.States[m.IssueID]
		out.Members = append(out.Members, result)
	}
	if normal && (out.Pending != nil || !inverse && out.Partial) {
		return nil, fmt.Errorf("successful paired command omitted or failed to verify an effect")
	}
	saved := root.Group.Forward
	if inverse {
		saved = root.Group.Inverse
	}
	if saved != nil && !equalGroupMemberResults(saved.Members, out.Members) {
		return nil, fmt.Errorf("paired command completed result changed")
	}
	return out, nil
}

func samePairedGroupObservation(a, b *pairedGroupObservation) bool {
	return maps.EqualFunc(a.States, b.States, func(x, y ActionIssueState) bool { return x.Revision == y.Revision }) && reflect.DeepEqual(a.Receipts, b.Receipts) && reflect.DeepEqual(a.Comments, b.Comments) && equalGroupMemberResults(a.Members, b.Members)
}

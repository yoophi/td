package ghstore

import (
	"context"
	"crypto/rand"
	"fmt"
	"maps"
	"reflect"
	"time"
)

// BeginHandoffReviewGroup persists the complete opaque plans before any native
// handoff or review write. It also groups a single tagged task.
func (c *Client) BeginHandoffReviewGroup(ctx context.Context, plans []*PreparedHandoffReview) error {
	if len(plans) == 0 || c.journal == nil || c.journal.Group != nil {
		return fmt.Errorf("handoff review requires its own tracked command")
	}
	g := &ActionGroup{Command: "ws-handoff-review", HandoffReview: &ActionHandoffReview{}}
	expected, sizing := map[string]ActionIssueState{}, map[string]ActionIssueState{}
	carrier := "gh-2147483647"
	for _, p := range plans {
		if err := c.checkPreparedHandoff(p); err != nil {
			return err
		}
		if p.commentAttempted || p.reviewAttempted {
			return fmt.Errorf("handoff review plan was already attempted")
		}
		before, err := actionIssueState(p.observed)
		if err != nil {
			return err
		}
		if _, dup := expected[p.observed.ID]; dup {
			return fmt.Errorf("handoff review repeats a task")
		}
		if p.observed.ID == carrier {
			carrier = "gh-2147483646"
		}
		member := ActionGroupMember{IssueID: p.observed.ID, Before: before}
		g.Members = append(g.Members, member)
		g.HandoffReview.Inputs = append(g.HandoffReview.Inputs, ActionHandoffReviewInput{Comment: &ActionCommentInput{Body: p.body, OperationID: p.data.OperationID}, Review: p.review})
		expected[p.observed.ID], sizing[p.observed.ID] = before, before
		if p.review != nil {
			intent, err := handoffReviewIntent(member, p.review, &ActionCommentState{ID: "ghc-9223372036854775807", UpdatedAt: time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC)})
			if err != nil {
				return err
			}
			state, err := intent.Target.contents()
			if err != nil {
				return err
			}
			state.UpdatedAt = before.UpdatedAt
			state.Revision, _ = state.checksum()
			sizing[p.observed.ID] = state
		}
	}
	root := ActionJournal{OperationID: "td-op-" + rand.Text(), SessionID: c.journal.SessionID, IssueID: g.Members[0].IssueID, CarrierID: carrier, Action: "group", Phase: "prepared", Group: g}
	if err := checkGroupArchiveCapacity(root, sizing); err != nil {
		return fmt.Errorf("handoff review capacity preflight (no write attempted): %w", err)
	}
	for sequence, i := range g.handoffEffects() {
		member, input := g.Members[i], g.HandoffReview.Inputs[i]
		effect := ActionJournal{OperationID: "td-op-" + rand.Text(), SessionID: root.SessionID, IssueID: member.IssueID, CarrierID: carrier, Phase: "prepared", Before: &member.Before, GroupContract: handoffReviewContract, GroupOperationID: root.OperationID, GroupSequence: sequence + 1}
		if sequence < len(g.Members) {
			effect.Action, effect.CommentInput = "comment_create", input.Comment
			if err := checkActivityGroupMemberCapacity(effect); err != nil {
				return err
			}
		} else {
			effect.Action, effect.GroupPreviousReceiptID = "update", "ghc-9223372036854775807"
			var err error
			effect.UpdateIntent, err = handoffReviewIntent(member, input.Review, &ActionCommentState{ID: "ghc-9223372036854775807", UpdatedAt: time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC)})
			if err != nil {
				return err
			}
			if err := checkUpdateArchiveCapacity(effect); err != nil {
				return err
			}
		}
	}
	if err := c.verifyGroupExpected(ctx, expected); err != nil {
		return err
	}
	if err := c.EnableActionJournal(ctx, root.SessionID); err != nil {
		return err
	}
	root.CarrierID = c.journal.Carrier.ID
	if err := c.verifyGroupExpected(ctx, expected); err != nil {
		return err
	}
	written, err := c.writePreparedJournal(ctx, root)
	if err != nil {
		return err
	}
	c.journal.Group = &activeActionGroup{Journal: written, Expected: expected, Attempted: map[string]bool{}}
	return nil
}

func (c *Client) bindPairedGroupMember(a *ActionJournal) error {
	active := c.journal.Group
	for _, r := range active.Receipts {
		if r.Phase != "applied" {
			return fmt.Errorf("paired command predecessor has an uncertain outcome")
		}
	}
	contract := handoffReviewContract
	if active.Journal.Group.updateCommentsGroup() {
		contract = updateCommentsContract
	}
	if active.Journal.Group.updateRelationsGroup() {
		contract = updateRelationsContract
	}
	a.GroupContract, a.GroupOperationID, a.GroupSequence, a.CarrierID = contract, active.Journal.OperationID, len(active.Receipts)+1, active.Journal.CarrierID
	chain := []*ActionJournalRecord{}
	for _, r := range active.Receipts {
		if r.IssueID == a.IssueID {
			chain = append(chain, r)
			a.GroupPreviousReceiptID = r.ID
		}
	}
	state := active.Expected[a.IssueID]
	probe := &ActionJournalRecord{ActionJournal: *a, Author: active.Journal.Author}
	if err := validateGroupMemberAtState(active.Journal, probe, &state); err != nil {
		return err
	}
	if active.Journal.Group.handoffReviewGroup() {
		return validateHandoffReviewBinding(active.Journal, probe, chain)
	}
	if active.Journal.Group.updateRelationsGroup() {
		return validateRelationUpdateBindings(active.Journal, probe, active.Expected)
	}
	return nil
}

func (c *Client) verifyPairedGroupEvents(ctx context.Context, root *ActionJournalRecord) error {
	if root.Group.fieldCommentGroup() {
		for i, input := range root.Group.fieldCommentInputs() {
			if input.Update == nil || input.Update.NativeEvents == nil {
				continue
			}
			id := root.Group.Members[i].IssueID
			current, err := c.readStateEvents(ctx, id)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(current, *input.Update.NativeEvents) {
				return fmt.Errorf("update/comments native state events changed on %s", id)
			}
		}
		return nil
	}
	for i, input := range root.Group.HandoffReview.Inputs {
		if input.Review == nil {
			continue
		}
		current, err := c.readStateEvents(ctx, root.Group.Members[i].IssueID)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(current, input.Review.Events) {
			return fmt.Errorf("handoff review native state events changed on %s", root.Group.Members[i].IssueID)
		}
	}
	return nil
}

func (c *Client) verifyPairedGroupForward(ctx context.Context, after *Record) error {
	active := c.journal.Group
	expected := maps.Clone(active.Expected)
	present := map[string]bool{}
	for _, r := range active.Receipts {
		if r.Action == "comment_create" && r.Phase == "applied" {
			present[r.IssueID] = true
		}
	}
	if after != nil {
		state, err := actionIssueState(after)
		if err != nil {
			return err
		}
		expected[after.ID] = state
		if len(active.Receipts) > 0 && active.Receipts[len(active.Receipts)-1].Action == "comment_create" {
			present[after.ID] = true
		}
	}
	return c.fencePairedGroup(ctx, active.Journal, active.Receipts, expected, present)
}

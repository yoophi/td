package ghstore

import (
	"encoding/hex"
	"fmt"
	"reflect"
	"slices"
)

const actionUpdateCommentsJournalStart = actionJournalPrefix + "v25\n"
const actionUpdateCommentsMemberStart = actionJournalPrefix + "v26\n"
const updateCommentsContract = "update-comments"

// Each selected task has an optional frozen scalar update and its own comment.
// Inputs describe requested contents; only actual receipts establish clocks.
type ActionUpdateComments struct {
	Inputs         []ActionUpdateActivity `json:"inputs"`
	ForwardDigests []string               `json:"forward_digests,omitempty"`
}

func (g *ActionGroup) updateCommentsGroup() bool { return g != nil && g.UpdateComments != nil }
func (g *ActionGroup) pairedGroup() bool         { return g.handoffReviewGroup() || g.fieldCommentGroup() }
func (g *ActionGroup) pairedEffectCount() int {
	if g.fieldCommentGroup() {
		return len(g.updateCommentEffects())
	}
	return len(g.handoffEffects())
}

type updateCommentEffect struct {
	member int
	action string
}

func (g *ActionGroup) updateCommentEffects() []updateCommentEffect {
	order := []updateCommentEffect{}
	inputs := g.fieldCommentInputs()
	indices := make([]int, len(inputs))
	for i := range indices {
		indices[i] = i
	}
	if g.updateRelationsGroup() {
		indices = g.RelationUpdates.Order
	}
	for _, i := range indices {
		input := inputs[i]
		if input.Update != nil {
			order = append(order, updateCommentEffect{i, "update"})
		}
		if input.Comment != nil {
			order = append(order, updateCommentEffect{i, "comment_create"})
		}
	}
	return order
}
func (g *ActionGroup) pairedComment(i int) *ActionCommentInput {
	if g.fieldCommentGroup() {
		return g.fieldCommentInputs()[i].Comment
	}
	return g.HandoffReview.Inputs[i].Comment
}
func (g *ActionGroup) pairedDigests() []string {
	if g.updateRelationsGroup() {
		return g.RelationUpdates.ForwardDigests
	}
	if g.updateCommentsGroup() {
		return g.UpdateComments.ForwardDigests
	}
	return g.HandoffReview.ForwardDigests
}
func (g *ActionGroup) setPairedDigests(digests []string) {
	if g.updateRelationsGroup() {
		input := *g.RelationUpdates
		input.ForwardDigests = digests
		g.RelationUpdates = &input
	} else if g.updateCommentsGroup() {
		input := *g.UpdateComments
		input.ForwardDigests = digests
		g.UpdateComments = &input
	} else {
		input := *g.HandoffReview
		input.ForwardDigests = digests
		g.HandoffReview = &input
	}
}

func (a ActionJournal) validateUpdateCommentsGroup() error {
	g := a.Group
	if !g.updateCommentsGroup() {
		return nil
	}
	if g.Command != "update-comments" || g.HandoffReview != nil || g.UpdateActivity != nil || g.Ordered != nil || g.Planned || g.Cascade || len(g.Roots) != 0 || len(g.UpdateComments.Inputs) != len(g.Members) {
		return fmt.Errorf("update/comments requires its distinct selected tasks and complete paired inputs")
	}
	operations := map[string]bool{}
	for i, input := range g.UpdateComments.Inputs {
		member := g.Members[i]
		if input.Comment == nil || input.Update != nil && len(input.Update.Related) != 0 || len(input.ForwardDigests) != 0 || member.Input != nil || member.CommentInput != nil || member.Noop {
			return fmt.Errorf("update/comments requires independent scalar and comment inputs")
		}
		requested := member.Before
		if input.Update != nil {
			update := ActionJournal{OperationID: "probe-update", SessionID: a.SessionID, IssueID: member.IssueID, CarrierID: a.CarrierID, Action: "update", Phase: "prepared", Before: &member.Before, UpdateIntent: input.Update}
			if err := update.validate(); err != nil {
				return err
			}
			var err error
			requested, err = input.Update.Target.contents()
			if err != nil {
				return err
			}
			// A private scope probe, never a persisted future observation.
			requested.UpdatedAt = member.Before.UpdatedAt
			requested.Revision, err = requested.checksum()
			if err != nil {
				return err
			}
			update.After = &requested
			if err := scalarFieldUndoScope(&ActionJournalRecord{ActionJournal: update}); err != nil {
				return err
			}
		}
		comment := ActionJournal{OperationID: "probe-comment", SessionID: a.SessionID, IssueID: member.IssueID, CarrierID: a.CarrierID, Action: "comment_create", Phase: "prepared", Before: &requested, CommentInput: input.Comment}
		if err := comment.validate(); err != nil {
			return err
		}
		activity, err := decodeActivity(apiComment{ID: 1, Body: input.Comment.Body}, member.IssueID)
		if err != nil || activity.Kind != "comment" || activity.SessionID != a.SessionID || operations[input.Comment.OperationID] {
			return fmt.Errorf("update/comments requires distinct attributed inline comments")
		}
		operations[input.Comment.OperationID] = true
	}
	digests := g.UpdateComments.ForwardDigests
	if len(digests) > g.pairedEffectCount() || a.Phase == "prepared" && len(digests) != 0 {
		return fmt.Errorf("update/comments has invalid original effect membership")
	}
	for _, digest := range digests {
		raw, err := hex.DecodeString(digest)
		if err != nil || len(raw) != 32 {
			return fmt.Errorf("invalid update/comments effect digest")
		}
	}
	return nil
}

func validateUpdateCommentsMember(root, member *ActionJournalRecord) error {
	g := root.Group
	if member.GroupContract != updateCommentsContract || member.GroupMixed || member.GroupSequence < 1 || member.GroupSequence > g.pairedEffectCount() {
		return fmt.Errorf("update/comments member has an undeclared effect")
	}
	effect := g.updateCommentEffects()[member.GroupSequence-1]
	i := effect.member
	input := g.UpdateComments.Inputs[i]
	if member.IssueID != g.Members[i].IssueID {
		return fmt.Errorf("update/comments member replaced its target or command order")
	}
	if effect.action == "update" {
		if member.Action != "update" || member.GroupPreviousReceiptID != "" || member.CommentInput != nil || !reflect.DeepEqual(member.UpdateIntent, input.Update) {
			return fmt.Errorf("update/comments field input differs from original plan")
		}
	} else if member.Action != "comment_create" || (member.GroupPreviousReceiptID != "") != (input.Update != nil) || member.UpdateIntent != nil || !reflect.DeepEqual(member.CommentInput, input.Comment) {
		return fmt.Errorf("update/comments comment input differs from original plan")
	}
	return member.validate()
}

func validateUpdateCommentsRewrite(old, next *ActionUpdateComments, completed bool) error {
	if old == nil || next == nil {
		if old != nil || next != nil {
			return fmt.Errorf("update/comments group identity is immutable")
		}
		return nil
	}
	if !reflect.DeepEqual(old.Inputs, next.Inputs) || completed && !slices.Equal(old.ForwardDigests, next.ForwardDigests) {
		return fmt.Errorf("update/comments requested inputs and original effects are immutable")
	}
	return nil
}

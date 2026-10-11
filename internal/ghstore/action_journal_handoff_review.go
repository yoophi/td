package ghstore

import (
	"context"
	"encoding/hex"
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/marcus/td/internal/models"
)

const actionHandoffReviewJournalStart = actionJournalPrefix + "v23\n"
const actionHandoffReviewMemberStart = actionJournalPrefix + "v24\n"
const handoffReviewContract = "handoff-review"

// Inputs are aligned with original Members. Comments run in that order, then
// reviews for the originally in-progress tasks. No future native IDs or clocks
// are stored in these templates.
type ActionHandoffReview struct {
	Inputs         []ActionHandoffReviewInput `json:"inputs"`
	ForwardDigests []string                   `json:"forward_digests,omitempty"`
}
type ActionHandoffReviewInput struct {
	Comment *ActionCommentInput    `json:"comment"`
	Review  *reviewSubmissionInput `json:"review,omitempty"`
}

func (g *ActionGroup) handoffReviewGroup() bool { return g != nil && g.HandoffReview != nil }
func (g *ActionGroup) sequencedGroup() bool {
	return g != nil && (g.Ordered != nil || g.updateActivityGroup() || g.pairedGroup())
}
func (g *ActionGroup) handoffEffects() []int {
	out := make([]int, len(g.Members))
	for i := range out {
		out[i] = i
	}
	for i, input := range g.HandoffReview.Inputs {
		if input.Review != nil {
			out = append(out, i)
		}
	}
	return out
}

// This pure projection runs the existing payload builder with no transport.
// Parent edits are explicitly excluded before it can request observations.
func handoffReviewIntent(member ActionGroupMember, input *reviewSubmissionInput, comment *ActionCommentState) (*ActionUpdateIntent, error) {
	original, err := actionStateRecord(&member.Before, member.IssueID)
	if err != nil {
		return nil, err
	}
	if input == nil || input.Details.ParentID != original.ParentID {
		return nil, fmt.Errorf("handoff review cannot replace parent relationships")
	}
	change, err := reviewSubmissionChanges(original, input, &models.Activity{ID: comment.ID, IssueID: member.IssueID, Kind: "handoff", UpdatedAt: comment.UpdatedAt})
	if err != nil {
		return nil, err
	}
	client := &Client{repo: original.repository, run: func(context.Context, string, []byte, ...string) ([]byte, error) {
		return nil, fmt.Errorf("handoff review projection cannot read remote state")
	}}
	plan, err := client.prepareIssueUpdate(context.Background(), original, change)
	if err != nil {
		return nil, err
	}
	target, err := updateInputFromPayload(member.Before, plan.payload)
	if err != nil {
		return nil, err
	}
	events := slices.Clone(input.Events)
	return &ActionUpdateIntent{Target: target, NativeEvents: &events}, nil
}

func (a ActionJournal) validateHandoffReviewGroup() error {
	g := a.Group
	if !g.handoffReviewGroup() {
		return nil
	}
	if g.Command != "ws-handoff-review" || g.UpdateActivity != nil || g.Ordered != nil || g.Planned || g.Cascade || len(g.Roots) != 0 || len(g.HandoffReview.Inputs) != len(g.Members) {
		return fmt.Errorf("handoff review requires complete original command inputs")
	}
	operation, workSession := "", ""
	for i, member := range g.Members {
		input := g.HandoffReview.Inputs[i]
		if member.Noop || member.Input != nil || member.CommentInput != nil || input.Comment == nil {
			return fmt.Errorf("handoff review requires one handoff per original task")
		}
		probe := ActionJournal{OperationID: "probe-comment", SessionID: a.SessionID, IssueID: member.IssueID, CarrierID: a.CarrierID, Action: "comment_create", Phase: "prepared", Before: &member.Before, CommentInput: input.Comment}
		if err := probe.validate(); err != nil {
			return err
		}
		activity, err := decodeActivity(apiComment{ID: 1, Body: input.Comment.Body}, member.IssueID)
		if err != nil || activity.Kind != "handoff" || activity.SessionID != a.SessionID || activity.WorkSessionID == "" {
			return fmt.Errorf("handoff review activity attribution differs")
		}
		if i == 0 {
			operation, workSession = activity.OperationID, activity.WorkSessionID
		}
		if activity.OperationID != operation || activity.WorkSessionID != workSession {
			return fmt.Errorf("handoff review changed its work-session operation")
		}
		original, err := actionStateRecord(&member.Before, member.IssueID)
		if err != nil {
			return err
		}
		if (input.Review != nil) != (original.Status == models.StatusInProgress) {
			return fmt.Errorf("handoff review changed its original review selection")
		}
		if input.Review == nil {
			continue
		}
		review := input.Review
		if review.Before.Revision != member.Before.Revision || !reflect.DeepEqual(review.Before, member.Before) || review.Details.ReviewHandoffID != "" || !review.Details.ReviewHandoffUpdatedAt.IsZero() || review.RequiresHandoff != !review.Details.Minor || review.Details.ReviewEvents != stateEventDigest(review.Events) {
			return fmt.Errorf("handoff review template replaced original evidence or invented a handoff")
		}
		// Private scope/sizing probe, never a saved native observation.
		intent, err := handoffReviewIntent(member, review, &ActionCommentState{ID: "ghc-9223372036854775807", UpdatedAt: time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)})
		if err != nil {
			return err
		}
		after, err := intent.Target.contents()
		if err != nil {
			return err
		}
		after.UpdatedAt = member.Before.UpdatedAt
		after.Revision, _ = after.checksum()
		effect := &ActionJournalRecord{ActionJournal: ActionJournal{OperationID: "probe-review", SessionID: a.SessionID, IssueID: member.IssueID, CarrierID: a.CarrierID, Action: "update", Phase: "prepared", Before: &member.Before, UpdateIntent: intent}}
		if err := effect.validate(); err != nil {
			return err
		}
		effect.After = &after
		ok, err := reviewTransitionUndoScope(effect)
		if err != nil || !ok {
			return fmt.Errorf("handoff review input is outside its review inverse contract: %v", err)
		}
	}
	digests := g.HandoffReview.ForwardDigests
	if len(digests) > len(g.handoffEffects()) || a.Phase == "prepared" && len(digests) != 0 {
		return fmt.Errorf("handoff review has invalid original effect membership")
	}
	for _, digest := range digests {
		raw, err := hex.DecodeString(digest)
		if err != nil || len(raw) != 32 {
			return fmt.Errorf("invalid handoff review effect digest")
		}
	}
	return nil
}

func validateHandoffReviewMember(root, member *ActionJournalRecord) error {
	g := root.Group
	order := g.handoffEffects()
	if member.GroupContract != handoffReviewContract || member.GroupMixed || member.GroupSequence < 1 || member.GroupSequence > len(order) {
		return fmt.Errorf("handoff review member has an undeclared effect")
	}
	i := order[member.GroupSequence-1]
	input := g.HandoffReview.Inputs[i]
	if member.IssueID != g.Members[i].IssueID {
		return fmt.Errorf("handoff review effect target or order changed")
	}
	if member.GroupSequence <= len(g.Members) {
		if member.Action != "comment_create" || member.GroupPreviousReceiptID != "" || !reflect.DeepEqual(member.CommentInput, input.Comment) || member.UpdateIntent != nil {
			return fmt.Errorf("handoff review comment differs from original input")
		}
	} else {
		if member.Action != "update" || member.GroupPreviousReceiptID == "" || member.CommentInput != nil || member.UpdateIntent == nil {
			return fmt.Errorf("handoff review requires its actual predecessor and complete review input")
		}
		_, meta, _, err := decodeBody(member.UpdateIntent.Target.Body)
		if err != nil || meta.Details == nil {
			return fmt.Errorf("handoff review missing review details")
		}
		intent, err := handoffReviewIntent(g.Members[i], input.Review, &ActionCommentState{ID: meta.Details.ReviewHandoffID, UpdatedAt: meta.Details.ReviewHandoffUpdatedAt})
		if err != nil || !reflect.DeepEqual(intent, member.UpdateIntent) {
			return fmt.Errorf("handoff review differs from its frozen review template: %v", err)
		}
	}
	return member.validate()
}

func validateHandoffReviewBinding(root, member *ActionJournalRecord, chain []*ActionJournalRecord) error {
	if member.Action != "update" {
		return nil
	}
	if len(chain) != 1 || chain[0].CommentAfter == nil {
		return fmt.Errorf("handoff review has no actual handoff result")
	}
	i := root.Group.handoffEffects()[member.GroupSequence-1]
	intent, err := handoffReviewIntent(root.Group.Members[i], root.Group.HandoffReview.Inputs[i].Review, chain[0].CommentAfter)
	if err != nil || !reflect.DeepEqual(intent, member.UpdateIntent) {
		return fmt.Errorf("review checkpoint differs from this command's actual handoff: %v", err)
	}
	return nil
}

func validatePairedGroupForward(root *ActionJournalRecord, receipts []*ActionJournalRecord) error {
	if root.Phase == "prepared" {
		return nil
	}
	digests := root.Group.pairedDigests()
	if len(digests) != len(receipts) {
		return fmt.Errorf("paired command original effect membership changed")
	}
	for i, r := range receipts {
		digest, err := updateActivityForwardDigest(r)
		if err != nil || digests[i] != digest {
			return fmt.Errorf("paired command original effect identity, input or result changed")
		}
	}
	return nil
}

func validateHandoffReviewRewrite(old, next *ActionHandoffReview, completed bool) error {
	if old == nil || next == nil {
		if old != nil || next != nil {
			return fmt.Errorf("handoff review identity is immutable")
		}
		return nil
	}
	if !reflect.DeepEqual(old.Inputs, next.Inputs) || completed && !slices.Equal(old.ForwardDigests, next.ForwardDigests) {
		return fmt.Errorf("handoff review inputs and original effects are immutable")
	}
	return nil
}

package ghstore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/marcus/td/internal/models"
)

const actionUpdateActivityJournalStart = actionJournalPrefix + "v21\n"
const actionUpdateActivityMemberJournalStart = actionJournalPrefix + "v22\n"

// Both requested effects are fixed before the first target write. The comment's
// observed Before is supplied only by the actual completed update receipt.
type ActionUpdateActivity struct {
	Update         *ActionUpdateIntent `json:"update"`
	Comment        *ActionCommentInput `json:"comment"`
	ForwardDigests []string            `json:"forward_digests,omitempty"`
}

func updateActivityForwardDigest(record *ActionJournalRecord) (string, error) {
	original := *record
	original.UpdatedAt = time.Time{}
	original.UndoOperationID, original.Restored, original.UndoObservation = "", nil, nil
	original.Phase = "prepared"
	if original.After != nil {
		original.Phase = "applied"
	}
	data, err := json.Marshal(original)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func (g *ActionGroup) updateActivityGroup() bool { return g != nil && g.UpdateActivity != nil }

func validateUpdateActivityForward(root *ActionJournalRecord, receipts []*ActionJournalRecord) error {
	if root.Phase == "prepared" {
		return nil
	}
	if len(receipts) != len(root.Group.UpdateActivity.ForwardDigests) {
		return fmt.Errorf("update/activity original effect membership changed")
	}
	for i, receipt := range receipts {
		digest, err := updateActivityForwardDigest(receipt)
		if err != nil || digest != root.Group.UpdateActivity.ForwardDigests[i] {
			return fmt.Errorf("update/activity original effect identity, input or result changed")
		}
	}
	return nil
}

func validateUpdateActivityRewrite(old, next *ActionUpdateActivity, completed bool) error {
	if old == nil || next == nil {
		if old != nil || next != nil {
			return fmt.Errorf("update/activity group identity is immutable")
		}
		return nil
	}
	if !reflect.DeepEqual(old.Update, next.Update) || !reflect.DeepEqual(old.Comment, next.Comment) || completed && !slices.Equal(old.ForwardDigests, next.ForwardDigests) {
		return fmt.Errorf("update/activity requested inputs and original effect digests are immutable")
	}
	return nil
}

func (a ActionJournal) validateUpdateActivityGroup() error {
	g := a.Group
	if !g.updateActivityGroup() {
		return nil
	}
	if !slices.Contains([]string{"link", "unlink", "due", "defer"}, g.Command) || len(g.Members) != 1 || g.Ordered != nil || g.Planned || g.Cascade || len(g.Roots) != 0 {
		return fmt.Errorf("update/activity group requires one original task and a supported command")
	}
	input, member := g.UpdateActivity, g.Members[0]
	if len(input.ForwardDigests) > 2 || a.Phase == "prepared" && len(input.ForwardDigests) != 0 || a.Phase != "prepared" && len(input.ForwardDigests) == 0 && g.Forward != nil && g.Forward.Members[0].Status != "unattempted" {
		return fmt.Errorf("update/activity result requires immutable original effect digests")
	}
	for _, value := range input.ForwardDigests {
		decoded, err := hex.DecodeString(value)
		if err != nil || len(decoded) != sha256.Size {
			return fmt.Errorf("invalid update/activity effect digest")
		}
	}
	if input.Update == nil || input.Comment == nil || len(input.Update.Related) != 0 {
		return fmt.Errorf("update/activity group requires complete independent field and comment inputs")
	}
	update := ActionJournal{OperationID: "probe-update", SessionID: a.SessionID, IssueID: member.IssueID, CarrierID: a.CarrierID,
		Action: "update", Phase: "prepared", Before: &member.Before, UpdateIntent: input.Update}
	if err := update.validate(); err != nil {
		return err
	}
	requested, err := input.Update.Target.contents()
	if err != nil {
		return err
	}
	// This is a private scope check, never a future observation in the journal.
	requested.UpdatedAt = member.Before.UpdatedAt
	requested.Revision, err = requested.checksum()
	if err != nil {
		return err
	}
	update.After = &requested
	probe := &ActionJournalRecord{ActionJournal: update}
	if g.Command == "link" || g.Command == "unlink" {
		supported, err := fileUndoScope(probe)
		if err != nil || !supported {
			return fmt.Errorf("update/activity file input is outside the linked-file inverse contract: %v", err)
		}
	} else if err := scalarFieldUndoScope(probe); err != nil {
		return err
	}
	comment := ActionJournal{OperationID: "probe-comment", SessionID: a.SessionID, IssueID: member.IssueID, CarrierID: a.CarrierID,
		Action: "comment_create", Phase: "prepared", Before: &requested, CommentInput: input.Comment}
	if err := comment.validate(); err != nil {
		return err
	}
	activity, err := decodeActivity(apiComment{ID: 1, Body: input.Comment.Body}, member.IssueID)
	if err != nil || activity.Kind != "log" || activity.LogType != models.LogTypeProgress {
		return fmt.Errorf("update/activity command requires its progress activity")
	}
	return nil
}

func validateUpdateActivityMember(group, member *ActionJournalRecord) error {
	input := group.Group.UpdateActivity
	if !member.GroupMixed || member.IssueID != group.IssueID {
		return fmt.Errorf("update/activity member differs from declared command")
	}
	switch member.GroupSequence {
	case 1:
		if member.GroupPreviousReceiptID != "" || member.Action != "update" || !reflect.DeepEqual(member.UpdateIntent, input.Update) || member.CommentInput != nil {
			return fmt.Errorf("update/activity field input differs from original plan")
		}
	case 2:
		if member.GroupPreviousReceiptID == "" || member.Action != "comment_create" || !reflect.DeepEqual(member.CommentInput, input.Comment) || member.UpdateIntent != nil {
			return fmt.Errorf("update/activity comment input differs from original plan")
		}
	default:
		return fmt.Errorf("update/activity command has an undeclared effect")
	}
	return member.validate()
}

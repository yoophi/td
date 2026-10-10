package ghstore

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func handoffReviewModel(t *testing.T) ActionJournal {
	t.Helper()
	f, _, input := handoffReviewFixture(t)
	plans, err := f.client.PrepareHandoffReview(context.Background(), []ActivityGroupInput{input}, TransitionOptions{Mode: "trusted", SessionID: "ses_actor", Reason: "Ready"})
	if err != nil {
		t.Fatal(err)
	}
	before, err := actionIssueState(input.Issue)
	if err != nil {
		t.Fatal(err)
	}
	return ActionJournal{OperationID: "group-handoff", SessionID: "ses_actor", CarrierID: "gh-9", IssueID: "gh-1", Action: "group", Phase: "prepared", Group: &ActionGroup{Command: "ws-handoff-review", Members: []ActionGroupMember{{IssueID: "gh-1", Before: before}}, HandoffReview: &ActionHandoffReview{Inputs: []ActionHandoffReviewInput{{Comment: &ActionCommentInput{Body: plans[0].body, OperationID: plans[0].data.OperationID}, Review: plans[0].review}}}}}
}
func TestHandoffReviewGroupTransportAndFrozenBinding(t *testing.T) {
	root := handoffReviewModel(t)
	before := root.Group.Members[0].Before
	comment := ActionJournal{OperationID: "member-handoff", SessionID: root.SessionID, CarrierID: root.CarrierID, IssueID: root.IssueID, Action: "comment_create", Phase: "prepared", Before: &before, CommentInput: root.Group.HandoffReview.Inputs[0].Comment, GroupOperationID: root.OperationID, GroupContract: handoffReviewContract, GroupSequence: 1}
	for _, a := range []ActionJournal{root, comment} {
		body, err := renderWriteActionJournal(a)
		if err != nil {
			t.Fatal(err)
		}
		start := actionHandoffReviewJournalStart
		if a.GroupContract != "" {
			start = actionHandoffReviewMemberStart
		}
		if !strings.Contains(body, start) {
			t.Fatal("new semantic version missing")
		}
		native := journalCommentFixture(t, a)
		native.Body = body
		decoded, err := decodeActionJournal(native, a.CarrierID)
		if err != nil || !reflect.DeepEqual(a, decoded.ActionJournal) {
			t.Fatal("round trip", err)
		}
		for _, old := range []string{actionActivityGroupJournalStart, actionUpdateActivityJournalStart, actionUpdateActivityMemberJournalStart, actionOrderedGroupMemberJournalStart, actionGroupJournalStart} {
			native.Body = strings.Replace(body, start, old, 1)
			if _, err := decodeActionJournal(native, a.CarrierID); err == nil {
				t.Fatal("legacy version accepted new semantics", old)
			}
		}
	}
	// Same valid template with a different native checkpoint is not this command.
	group := &ActionJournalRecord{ActionJournal: root, ID: "ghc-100", Author: "github-author", CreatedAt: before.UpdatedAt}
	actual := &ActionCommentState{ID: "ghc-201", IssueID: root.IssueID, Body: comment.CommentInput.Body, Author: group.Author, CreatedAt: before.UpdatedAt.Add(time.Second), UpdatedAt: before.UpdatedAt.Add(time.Second)}
	actual.Revision, _ = actual.checksum()
	after := before
	after.UpdatedAt = actual.UpdatedAt
	after.Revision, _ = after.checksum()
	first := &ActionJournalRecord{ActionJournal: comment, ID: "ghc-101", Author: group.Author, CreatedAt: group.CreatedAt}
	first.Phase, first.After, first.CommentAfter = "applied", &after, actual
	intent, err := handoffReviewIntent(root.Group.Members[0], root.Group.HandoffReview.Inputs[0].Review, actual)
	if err != nil {
		t.Fatal(err)
	}
	review := &ActionJournalRecord{ActionJournal: ActionJournal{OperationID: "member-review", SessionID: root.SessionID, CarrierID: root.CarrierID, IssueID: root.IssueID, Action: "update", Phase: "prepared", Before: &after, UpdateIntent: intent, GroupOperationID: root.OperationID, GroupContract: handoffReviewContract, GroupSequence: 2, GroupPreviousReceiptID: first.ID}, ID: "ghc-202", Author: group.Author, CreatedAt: actual.CreatedAt}
	if _, err := orderedGroupEffects(group, []*ActionJournalRecord{review, first}); err != nil {
		t.Fatal(err)
	}
	foreign := *actual
	foreign.ID = "ghc-203"
	review.UpdateIntent, err = handoffReviewIntent(root.Group.Members[0], root.Group.HandoffReview.Inputs[0].Review, &foreign)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := orderedGroupEffects(group, []*ActionJournalRecord{first, review}); err == nil {
		t.Fatal("foreign native checkpoint accepted")
	}
	large := handoffReviewModel(t)
	input := large.Group.HandoffReview.Inputs[0]
	input.Comment.Body = strings.ReplaceAll(input.Comment.Body, "Implemented", strings.Repeat("Large handoff text ", 3500))
	body, err := renderWriteActionJournal(large)
	if err != nil || !strings.Contains(body, actionCompressedJournalStart) {
		t.Fatal("compressed group", err)
	}
	native := journalCommentFixture(t, large)
	native.Body = body
	decoded, err := decodeActionJournal(native, large.CarrierID)
	if err != nil || !reflect.DeepEqual(large, decoded.ActionJournal) {
		t.Fatal("compressed round trip", err)
	}
}
func TestHandoffReviewGroupRejectsReplacedInputs(t *testing.T) {
	for _, mode := range []string{"selection", "body", "actor", "future-comment", "scope", "event-digest", "original-before", "unknown-contract"} {
		t.Run(mode, func(t *testing.T) {
			root := handoffReviewModel(t)
			raw, _ := json.Marshal(root)
			var next ActionJournal
			if err := json.Unmarshal(raw, &next); err != nil {
				t.Fatal(err)
			}
			input := &next.Group.HandoffReview.Inputs[0]
			switch mode {
			case "selection":
				input.Review = nil
			case "body":
				input.Comment.Body = "Forged activity"
			case "actor":
				input.Review.Details.ReviewRequestedBySession = "foreign-session"
			case "future-comment":
				input.Review.Details.ReviewHandoffID = "ghc-123"
			case "scope":
				input.Review.Details.ParentID = "gh-33"
			case "event-digest":
				input.Review.Details.ReviewEvents = strings.Repeat("a", 64)
			case "original-before":
				input.Review.Before.Title = "Changed"
			case "unknown-contract":
				next.GroupContract = "future"
			}
			if _, err := renderWriteActionJournal(next); err == nil {
				t.Fatal("invalid command input accepted", mode)
			}
			if mode != "unknown-contract" && validateGroupRewrite(root, next) == nil {
				t.Fatal("immutable command input replaced")
			}
		})
	}
}

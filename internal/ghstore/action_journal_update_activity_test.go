package ghstore

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/marcus/td/internal/models"
)

func updateActivityModelFixture(t *testing.T) ActionJournal {
	t.Helper()
	a := plannedUpdateModelFixture(t)
	input := a.Group.Members[0].Input
	a.Group.Members = a.Group.Members[:1]
	a.Group.Members[0].Input = nil
	a.Group.Command = "due"
	body, err := renderActivity(activityData{Kind: "log", OperationID: "activity-operation", SessionID: a.SessionID, Message: "Due date changed", LogType: "progress"})
	if err != nil {
		t.Fatal(err)
	}
	a.Group.UpdateActivity = &ActionUpdateActivity{Update: input, Comment: &ActionCommentInput{Body: body, OperationID: "activity-operation"}}
	return a
}

func TestUpdateActivityPreflightValidatesCommentBeforeAnyWrite(t *testing.T) {
	f := newJournalWriteFixture(t, "")
	if err := f.client.TrackActions("ses_actor"); err != nil {
		t.Fatal(err)
	}
	observed, err := f.client.Get(context.Background(), "gh-1")
	if err != nil {
		t.Fatal(err)
	}
	date := "2099-01-01"
	for _, message := range []string{"", strings.Repeat("x", actionCommentBudget+1)} {
		_, err := f.client.UpdateWithActivityObserved(context.Background(), observed, Changes{DueDate: &date}, models.Activity{
			Kind: "log", SessionID: "ses_actor", Message: message, LogType: models.LogTypeProgress}, "due")
		if err == nil || f.targetWrites != 0 || f.intentWrites != 0 || f.carrierWrites != 0 {
			t.Fatal("invalid later effect allowed an earlier write", err)
		}
	}
}

func TestUpdateActivityGroupTransportAndFrozenInputs(t *testing.T) {
	a := updateActivityModelFixture(t)
	before := a.Group.Members[0].Before
	m := ActionJournal{OperationID: "member-operation", SessionID: a.SessionID, CarrierID: a.CarrierID, IssueID: a.IssueID,
		Action: "update", Phase: "prepared", Before: &before, UpdateIntent: a.Group.UpdateActivity.Update,
		GroupOperationID: a.OperationID, GroupMixed: true, GroupSequence: 1}
	for _, input := range []ActionJournal{a, m} {
		body, err := renderWriteActionJournal(input)
		if err != nil {
			t.Fatal(err)
		}
		start := actionUpdateActivityJournalStart
		if input.GroupMixed {
			start = actionUpdateActivityMemberJournalStart
		}
		if !strings.Contains(body, start) || strings.Contains(body, actionMultipartJournalStart) {
			t.Fatal("semantic record reused multipart manifest framing")
		}
		comment := journalCommentFixture(t, input)
		comment.Body = body
		decoded, err := decodeActionJournal(comment, input.CarrierID)
		if err != nil || !reflect.DeepEqual(input, decoded.ActionJournal) {
			t.Fatal("update/activity round trip", err)
		}
		for _, legacy := range []string{actionGroupJournalStart, actionOrderedGroupMemberJournalStart, actionActivityGroupJournalStart, actionMultipartJournalStart} {
			comment.Body = strings.Replace(body, start, legacy, 1)
			if _, err := decodeActionJournal(comment, input.CarrierID); err == nil {
				t.Fatal("legacy framing accepted new command semantics", legacy)
			}
		}
	}
	if err := checkGroupArchiveCapacity(a, map[string]ActionIssueState{a.IssueID: before}); err != nil {
		t.Fatal("capacity projection failed", err)
	}
	if len(a.Group.UpdateActivity.ForwardDigests) != 0 || a.Group.Forward != nil {
		t.Fatal("private capacity estimates escaped into persisted inputs")
	}
	changed := updateActivityModelFixture(t)
	changed.Group.UpdateActivity.Comment.Body += "Edited"
	if validateGroupRewrite(a, changed) == nil {
		t.Fatal("original complete comment input was rewritten")
	}
	large := updateActivityModelFixture(t)
	large.Group.UpdateActivity.Comment.Body = strings.ReplaceAll(large.Group.UpdateActivity.Comment.Body, "Due date changed", strings.Repeat("Repeated activity text ", 4000))
	body, err := renderWriteActionJournal(large)
	if err != nil || !strings.Contains(body, actionCompressedJournalStart) {
		t.Fatal("compressed mixed group transport", err)
	}
	comment := journalCommentFixture(t, large)
	comment.Body = body
	decoded, err := decodeActionJournal(comment, large.CarrierID)
	if err != nil || !reflect.DeepEqual(large, decoded.ActionJournal) {
		t.Fatal("compressed mixed group round trip", err)
	}
}

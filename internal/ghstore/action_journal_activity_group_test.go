package ghstore

import (
	"context"
	"crypto/sha256"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/marcus/td/internal/models"
)

func activityGroupModelFixture(t *testing.T) ActionJournal {
	t.Helper()
	a := groupModelFixture(t)
	a.Group.Command = "ws-log"
	body, err := renderActivity(activityData{Kind: "log", OperationID: "shared-operation", SessionID: a.SessionID, WorkSessionID: "ws_original", Message: "Shared log", LogType: "progress"})
	if err != nil {
		t.Fatal(err)
	}
	for i := range a.Group.Members {
		a.Group.Members[i].CommentInput = &ActionCommentInput{Body: body, OperationID: "shared-operation"}
	}
	return a
}

func TestActivityGroupJournalVersionAndImmutableInputs(t *testing.T) {
	a := activityGroupModelFixture(t)
	member := a.Group.Members[0]
	m := ActionJournal{OperationID: "member-operation", SessionID: a.SessionID, CarrierID: a.CarrierID, IssueID: member.IssueID, Action: "comment_create", Phase: "prepared", Before: &member.Before, CommentInput: member.CommentInput, GroupOperationID: a.OperationID}
	for _, input := range []ActionJournal{a, m} {
		body, err := renderWriteActionJournal(input)
		if err != nil || !strings.Contains(body, actionActivityGroupJournalStart) {
			t.Fatal("activity group needs v19", err)
		}
		comment := journalCommentFixture(t, input)
		comment.Body = body
		decoded, err := decodeActionJournal(comment, a.CarrierID)
		if err != nil || !reflect.DeepEqual(input, decoded.ActionJournal) {
			t.Fatal("v19 round trip", err)
		}
		for _, legacy := range []string{actionGroupJournalStart, actionGroupMemberJournalStart, actionCommentJournalStart, actionCommentObservedJournalStart} {
			comment.Body = strings.Replace(body, actionActivityGroupJournalStart, legacy, 1)
			if _, err := decodeActionJournal(comment, a.CarrierID); err == nil {
				t.Fatal("legacy transport accepted activity group", legacy)
			}
		}
	}
	large := activityGroupModelFixture(t)
	for i := range large.Group.Members {
		input := large.Group.Members[i].CommentInput
		input.Body = strings.ReplaceAll(input.Body, "Shared log", strings.Repeat("Large saved log ", 5000))
	}
	body, err := renderWriteActionJournal(large)
	if err != nil || !strings.Contains(body, actionCompressedJournalStart) {
		t.Fatal("large activity group should use compressed transport", err)
	}
	comment := journalCommentFixture(t, large)
	comment.Body = body
	decoded, err := decodeActionJournal(comment, large.CarrierID)
	if err != nil || !reflect.DeepEqual(large, decoded.ActionJournal) {
		t.Fatal("compressed v19 round trip", err)
	}
	for name, mutate := range map[string]func(*ActionJournal){
		"missing-first":  func(a *ActionJournal) { a.Group.Members[0].CommentInput = nil },
		"missing-second": func(a *ActionJournal) { a.Group.Members[1].CommentInput = nil },
		"operation": func(a *ActionJournal) {
			a.Group.Members[1].CommentInput.Body = strings.ReplaceAll(a.Group.Members[1].CommentInput.Body, "shared-operation", "different-operation")
			a.Group.Members[1].CommentInput.OperationID = "different-operation"
		},
		"work-session": func(a *ActionJournal) {
			a.Group.Members[1].CommentInput.Body = strings.ReplaceAll(a.Group.Members[1].CommentInput.Body, "ws_original", "ws_foreign")
		},
		"actor-session": func(a *ActionJournal) {
			a.Group.Members[1].CommentInput.Body = strings.ReplaceAll(a.Group.Members[1].CommentInput.Body, a.SessionID, "ses_foreign")
		},
		"kind":       func(a *ActionJournal) { a.Group.Command = "ws-handoff" },
		"task-input": func(a *ActionJournal) { a.Group.Members[1].Input = &ActionUpdateIntent{} },
		"noop":       func(a *ActionJournal) { a.Group.Members[1].Noop = true },
		"planned":    func(a *ActionJournal) { a.Group.Planned = true },
		"ordered":    func(a *ActionJournal) { a.Group.Ordered = &ActionOrderedGroup{} },
	} {
		t.Run(name, func(t *testing.T) {
			changed := activityGroupModelFixture(t)
			mutate(&changed)
			if _, err := renderWriteActionJournal(changed); err == nil {
				t.Fatal("invalid activity input accepted")
			}
		})
	}
}

func TestActivityGroupRejectsInvalidSelectionBeforeWrites(t *testing.T) {
	for _, mode := range []string{"stale", "missing", "duplicate", "input", "capacity"} {
		t.Run(mode, func(t *testing.T) {
			f := newJournalWriteFixture(t, "")
			delete(f.issues, 9) // Even journal bootstrap must not happen.
			second := f.issues[1]
			second.Number = 2
			f.issues[2] = second
			ctx := context.Background()
			if err := f.client.TrackActions("ses_actor"); err != nil {
				t.Fatal(err)
			}
			inputs := []ActivityGroupInput{}
			for _, id := range []string{"gh-1", "gh-2"} {
				record, err := f.client.Get(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				inputs = append(inputs, ActivityGroupInput{Issue: record, Activity: models.Activity{Kind: "log", OperationID: "shared-operation", SessionID: "ses_actor", WorkSessionID: "ws_original", Message: "Shared log", LogType: models.LogTypeProgress}})
			}
			switch mode {
			case "stale":
				second.Title = "Concurrent edit"
				f.issues[2] = second
			case "missing":
				delete(f.issues, 2)
			case "duplicate":
				inputs[1] = inputs[0]
			case "input":
				inputs[1].Activity.WorkSessionID = ""
			case "capacity":
				var content strings.Builder
				for i := range 2500 {
					fmt.Fprintf(&content, "%x", sha256.Sum256([]byte(fmt.Sprint(i))))
				}
				inputs[1].Activity.Message = content.String()
			}
			err := f.client.BeginActivityGroup(ctx, "ws-log", inputs)
			if err == nil || f.carrierWrites != 0 || f.intentWrites != 0 || f.receiptWrites != 0 || f.targetWrites != 0 {
				t.Fatal("invalid selection wrote remote state", err, f)
			}
			if mode == "capacity" && !strings.Contains(err.Error(), "budget") {
				t.Fatal("large incompressible activity did not reach capacity check", err)
			}
		})
	}
}

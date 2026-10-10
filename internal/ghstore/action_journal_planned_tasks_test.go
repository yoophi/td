package ghstore

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/marcus/td/internal/models"
)

func plannedDeletionModel(t *testing.T, command string) ActionJournal {
	t.Helper()
	f := newJournalWriteFixture(t, "")
	second := f.issues[1]
	second.Number = 2
	f.issues[2] = second
	g := &ActionGroup{Command: command, Planned: true}
	for _, id := range []string{"gh-1", "gh-2"} {
		r, err := f.client.Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := f.client.prepareDeletion(context.Background(), r, command == "delete", "ses_actor", "Fixture")
		if err != nil {
			t.Fatal(err)
		}
		before, err := actionIssueState(r)
		if err != nil {
			t.Fatal(err)
		}
		input, err := f.client.plannedUpdateIntent(context.Background(), r, plan.payload, nil)
		if err != nil {
			t.Fatal(err)
		}
		g.Members = append(g.Members, ActionGroupMember{IssueID: id, Before: before, Input: input, Noop: plan.noop})
	}
	return ActionJournal{OperationID: "td-op-planned", SessionID: "ses_actor", IssueID: "gh-1", CarrierID: "gh-9", Action: "group", Phase: "prepared", Group: g}
}

func TestPlannedTaskGroupNoopAndVersionBoundaries(t *testing.T) {
	for _, command := range []string{"delete", "restore"} {
		a := plannedDeletionModel(t, command)
		body, err := renderWriteActionJournal(a)
		if err != nil || !strings.Contains(body, actionPlannedTaskGroupJournalStart) {
			t.Fatal(command, body, err)
		}
		comment := journalCommentFixture(t, a)
		comment.Body = body
		r, err := decodeActionJournal(comment, a.CarrierID)
		if err != nil || !reflect.DeepEqual(r.ActionJournal, a) {
			t.Fatal("v17 round trip", err)
		}
		comment.Body = strings.Replace(body, actionPlannedTaskGroupJournalStart, actionPlannedUpdateGroupJournalStart, 1)
		if _, err := decodeActionJournal(comment, a.CarrierID); err == nil {
			t.Fatal("v16 accepted planned command scope")
		}
	}
	for name, change := range map[string]func(*ActionJournal){
		"noop":          func(a *ActionJournal) { a.Group.Members[0].Noop = true },
		"command":       func(a *ActionJournal) { a.Group.Command = "restore" },
		"title":         func(a *ActionJournal) { a.Group.Members[0].Input.Target.Title = "Unrelated edit" },
		"session":       func(a *ActionJournal) { a.SessionID = "ses_foreign" },
		"undeclared":    func(a *ActionJournal) { a.Group.Planned = false },
		"missing-input": func(a *ActionJournal) { a.Group.Members[1].Input = nil },
	} {
		t.Run(name, func(t *testing.T) {
			a := plannedDeletionModel(t, "delete")
			change(&a)
			if _, err := renderWriteActionJournal(a); err == nil {
				t.Fatal("invalid planned effect accepted")
			}
		})
	}
	a := plannedDeletionModel(t, "restore")
	for i := range a.Group.Members {
		m := &a.Group.Members[i]
		m.Before.Body = strings.Repeat("Existing untouched task\n", 4000)
		m.Before.Revision, _ = m.Before.checksum()
		m.Input.Target.Body = m.Before.Body
	}
	body, err := renderWriteActionJournal(a)
	if err != nil || !strings.Contains(body, actionCompressedJournalStart) {
		t.Fatal("v17 compressed transport", err)
	}
	comment := journalCommentFixture(t, a)
	comment.Body = body
	r, err := decodeActionJournal(comment, a.CarrierID)
	if err != nil || !reflect.DeepEqual(r.ActionJournal, a) {
		t.Fatal("v17 compressed round trip", err)
	}
}

func TestDeletionPlanAcceptsLegacyMetadataWithoutRewritingWorkflow(t *testing.T) {
	for _, details := range []*IssueDetails{nil, {}, {Status: models.StatusInReview}} {
		f := newJournalWriteFixture(t, "")
		raw := f.issues[1]
		var err error
		if details != nil {
			if details.Status == models.StatusInReview {
				raw.State = "closed" // Native GitHub change outranks older detailed state.
			}
			raw.Body, err = encodeBody("Before", metadata{Type: models.TypeTask, Priority: models.PriorityP2, Details: details})
			if err != nil {
				t.Fatal(err)
			}
			f.issues[1] = raw
		}
		r, err := f.client.Get(context.Background(), "gh-1")
		if err != nil {
			t.Fatal(err)
		}
		plan, err := f.client.prepareDeletion(context.Background(), r, true, "ses_actor", "Legacy task")
		if err != nil {
			t.Fatal(err)
		}
		input, err := f.client.plannedUpdateIntent(context.Background(), r, plan.payload, nil)
		if err != nil {
			t.Fatal(err)
		}
		before, _ := actionIssueState(r)
		after, _ := input.Target.contents()
		if err := plannedTaskEffectScope("delete", &ActionJournalRecord{ActionJournal: ActionJournal{
			Action: "update", IssueID: r.ID, SessionID: "ses_actor", Before: &before, After: &after,
		}}); err != nil {
			t.Fatal("legacy deletion is not invertible", err)
		}
	}
}

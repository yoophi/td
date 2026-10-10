package ghstore

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func plannedUpdateModelFixture(t *testing.T) ActionJournal {
	t.Helper()
	a := groupModelFixture(t)
	a.Group.Command = "update"
	for i := range a.Group.Members {
		m := &a.Group.Members[i]
		m.Input = &ActionUpdateIntent{Target: ActionUpdateInput{Title: "Changed", Body: m.Before.Body, State: m.Before.State, Labels: append([]string{}, m.Before.Labels...)}}
	}
	return a
}

func TestPlannedUpdateGroupStrictInputsAndTransport(t *testing.T) {
	a := plannedUpdateModelFixture(t)
	body, err := renderWriteActionJournal(a)
	if err != nil || !strings.Contains(body, actionPlannedUpdateGroupJournalStart) {
		t.Fatal(body, err)
	}
	comment := journalCommentFixture(t, a)
	comment.Body = body
	r, err := decodeActionJournal(comment, a.CarrierID)
	if err != nil || !reflect.DeepEqual(r.ActionJournal, a) {
		t.Fatal("planned group round trip", err)
	}
	comment.Body = strings.Replace(body, actionPlannedUpdateGroupJournalStart, actionGroupJournalStart, 1)
	if _, err := decodeActionJournal(comment, a.CarrierID); err == nil {
		t.Fatal("legacy root accepted planned update payload")
	}
	for name, mutate := range map[string]func(*ActionJournal){
		"missing":  func(a *ActionJournal) { a.Group.Members[1].Input = nil },
		"state":    func(a *ActionJournal) { a.Group.Members[1].Input.Target.State = "closed" },
		"labels":   func(a *ActionJournal) { a.Group.Members[1].Input.Target.Labels = nil },
		"workflow": func(a *ActionJournal) { a.Group.Command = "start" },
		"ordered":  func(a *ActionJournal) { a.Group.Ordered = &ActionOrderedGroup{} },
		"related": func(a *ActionJournal) {
			a.Group.Members[1].Input.Related = []ActionRelatedObservation{{ID: "gh-4", State: a.Group.Members[0].Before}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			invalid := plannedUpdateModelFixture(t)
			mutate(&invalid)
			if _, err := renderWriteActionJournal(invalid); err == nil {
				t.Fatal("invalid planned group accepted")
			}
		})
	}
	changed := plannedUpdateModelFixture(t)
	changed.Group.Members[1].Input.Target.Title = "Different plan"
	if err := validateGroupRewrite(a, changed); err == nil {
		t.Fatal("immutable complete input was rewritten")
	}
	for i := range a.Group.Members {
		m := &a.Group.Members[i]
		m.Before.Body = strings.Repeat("Large planned update\n", 4000)
		m.Before.Revision, _ = m.Before.checksum()
		m.Input.Target.Body = m.Before.Body + "Appended"
	}
	body, err = renderWriteActionJournal(a)
	if err != nil || !strings.Contains(body, actionCompressedJournalStart) {
		t.Fatal("planned compressed transport", err)
	}
	comment.Body = body
	r, err = decodeActionJournal(comment, a.CarrierID)
	if err != nil || !reflect.DeepEqual(r.ActionJournal, a) {
		t.Fatal("planned compressed round trip", err)
	}
}

func TestPlannedUpdateMemberCannotChangeFrozenInput(t *testing.T) {
	a := plannedUpdateModelFixture(t)
	group := &ActionJournalRecord{ActionJournal: a, ID: "ghc-1", Author: "author"}
	m := a.Group.Members[0]
	member := &ActionJournalRecord{ActionJournal: ActionJournal{Action: "update", GroupOperationID: a.OperationID,
		IssueID: m.IssueID, SessionID: a.SessionID, CarrierID: a.CarrierID, Before: &m.Before, UpdateIntent: m.Input}, Author: "author"}
	if err := validateGroupMember(group, member); err != nil {
		t.Fatal(err)
	}
	changed := *m.Input
	changed.Target.Title = "Unexpected but otherwise valid update"
	member.UpdateIntent = &changed
	if err := validateGroupMember(group, member); err == nil {
		t.Fatal("member silently changed frozen command input")
	}
}

func TestPlannedUpdatePreflightRejectsStaleMissingAndDuplicateTargets(t *testing.T) {
	for _, mode := range []string{"stale", "missing", "duplicate", "invalid-fields"} {
		t.Run(mode, func(t *testing.T) {
			f := newJournalWriteFixture(t, "")
			second := f.issues[1]
			second.Number, second.Title = 2, "Second"
			f.issues[2] = second
			ctx := context.Background()
			if err := f.client.TrackActions("ses_actor"); err != nil {
				t.Fatal(err)
			}
			title := "Updated"
			var err error
			switch mode {
			case "stale":
				selected := []Record{}
				for _, id := range []string{"gh-1", "gh-2"} {
					r, readErr := f.client.Get(ctx, id)
					if readErr != nil {
						t.Fatal(readErr)
					}
					selected = append(selected, *r)
				}
				second.Title = "Concurrent edit"
				f.issues[2] = second
				_, err = f.client.updateManyObserved(ctx, selected, Changes{Title: &title})
				var conflict *ConflictError
				if !errors.As(err, &conflict) || conflict.ID != "gh-2" {
					t.Fatal("selection was rebased", err)
				}
			case "missing":
				_, err = f.client.UpdateMany(ctx, []string{"gh-1", "gh-3"}, Changes{Title: &title})
			case "duplicate":
				_, err = f.client.UpdateMany(ctx, []string{"gh-1", "1"}, Changes{Title: &title})
			case "invalid-fields":
				title = ""
				_, err = f.client.UpdateMany(ctx, []string{"gh-1", "gh-2"}, Changes{Title: &title})
			}
			if err == nil || f.targetWrites != 0 || f.intentWrites != 0 || f.carrierWrites != 0 || f.receiptWrites != 0 {
				t.Fatal("invalid batch caused a remote write", err, f)
			}
		})
	}
}

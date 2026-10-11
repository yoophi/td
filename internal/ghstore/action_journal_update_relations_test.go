package ghstore

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func relationUpdateModel(t *testing.T) ActionJournal {
	t.Helper()
	a := updateCommentsModel(t)
	g := a.Group
	g.Command = updateRelationsContract
	g.RelationUpdates = &ActionRelationUpdates{Inputs: g.UpdateComments.Inputs, Order: []int{0, 1}}
	g.UpdateComments = nil
	for i, m := range g.Members {
		g.RelationUpdates.Selected = append(g.RelationUpdates.Selected, m.IssueID)
		g.RelationUpdates.Inputs[i].Comment = nil
	}
	first := g.Members[0]
	g.RelationUpdates.Inputs[1].Update.Related = []ActionRelatedObservation{{ID: first.IssueID, State: first.Before}}
	return a
}

func TestUpdateRelationsFormatAndActualGraphBinding(t *testing.T) {
	a := relationUpdateModel(t)
	body, err := renderWriteActionJournal(a)
	if err != nil || !strings.Contains(body, actionUpdateRelationsJournalStart) {
		t.Fatal("new framing", err)
	}
	comment := journalCommentFixture(t, a)
	comment.Body = body
	decoded, err := decodeActionJournal(comment, a.CarrierID)
	if err != nil || !reflect.DeepEqual(decoded.ActionJournal, a) {
		t.Fatal("round trip", err)
	}
	for _, start := range []string{actionUpdateCommentsJournalStart, actionPlannedUpdateGroupJournalStart} {
		comment.Body = strings.Replace(body, actionUpdateRelationsJournalStart, start, 1)
		if _, err := decodeActionJournal(comment, a.CarrierID); err == nil {
			t.Fatal("legacy framing accepted")
		}
	}
	clock := a.Group.Members[0].Before.UpdatedAt
	root := &ActionJournalRecord{ActionJournal: a, ID: "ghc-100", Author: "author", CreatedAt: clock, UpdatedAt: clock}
	members := []*ActionJournalRecord{}
	for i, m := range a.Group.Members {
		input := *a.Group.RelationUpdates.Inputs[i].Update
		if i == 1 {
			input.Related = []ActionRelatedObservation{{ID: members[0].IssueID, State: *members[0].After}}
		}
		after, err := input.Target.contents()
		if err != nil {
			t.Fatal(err)
		}
		after.UpdatedAt = clock.Add(time.Duration(i+2) * time.Second)
		after.Revision, _ = after.checksum()
		id := "ghc-101"
		if i == 1 {
			id = "ghc-102"
		}
		members = append(members, &ActionJournalRecord{ActionJournal: ActionJournal{OperationID: "update-" + id, SessionID: a.SessionID, IssueID: m.IssueID, CarrierID: a.CarrierID, Action: "update", Phase: "applied", Before: &m.Before, After: &after, UpdateIntent: &input, GroupContract: updateRelationsContract, GroupOperationID: a.OperationID, GroupSequence: i + 1}, ID: id, Author: root.Author, CreatedAt: clock.Add(time.Duration(i+1) * time.Second), UpdatedAt: after.UpdatedAt})
	}
	if _, err := orderedGroupEffects(root, members); err != nil {
		t.Fatal("actual prior graph state", err)
	}
	memberComment := journalCommentFixture(t, members[1].ActionJournal)
	memberComment.Body, err = renderWriteActionJournal(members[1].ActionJournal)
	if err != nil || !strings.Contains(memberComment.Body, actionUpdateRelationsMemberStart) {
		t.Fatal("member framing", err)
	}
	if _, err := decodeActionJournal(memberComment, a.CarrierID); err != nil {
		t.Fatal(err)
	}
	members[1].UpdateIntent.Related[0].State = a.Group.Members[0].Before
	if _, err := orderedGroupEffects(root, members); err == nil {
		t.Fatal("stale prior graph state accepted")
	}
	large := relationUpdateModel(t)
	for i := range large.Group.Members {
		m := &large.Group.Members[i]
		m.Before.Body = strings.Repeat("Original task content\n", 4000)
		m.Before.Revision, _ = m.Before.checksum()
		large.Group.RelationUpdates.Inputs[i].Update.Target.Body = m.Before.Body
	}
	large.Group.RelationUpdates.Inputs[1].Update.Related[0].State = large.Group.Members[0].Before
	comment.Body, err = renderWriteActionJournal(large)
	if err != nil || !strings.Contains(comment.Body, actionCompressedJournalStart) {
		t.Fatal("compressed graph", err)
	}
	decoded, err = decodeActionJournal(comment, a.CarrierID)
	if err != nil || !reflect.DeepEqual(decoded.ActionJournal, large) {
		t.Fatal("compressed round trip", err)
	}
}

func TestUpdateRelationsRejectsReplacedPlan(t *testing.T) {
	for name, mutate := range map[string]func(*ActionJournal){
		"repeated order": func(a *ActionJournal) { a.Group.RelationUpdates.Order[1] = 0 },
		"missing order":  func(a *ActionJournal) { a.Group.RelationUpdates.Order = []int{0} },
		"foreign original graph": func(a *ActionJournal) {
			state := &a.Group.RelationUpdates.Inputs[1].Update.Related[0].State
			state.Title = "Foreign graph state"
			state.Revision, _ = state.checksum()
		},
		"undeclared reverse member": func(a *ActionJournal) { a.Group.RelationUpdates.Blocked = map[string][]string{a.IssueID: {"gh-999"}} },
		"native state transition":   func(a *ActionJournal) { a.Group.RelationUpdates.Inputs[0].Update.Target.State = "closed" },
	} {
		t.Run(name, func(t *testing.T) {
			a := relationUpdateModel(t)
			mutate(&a)
			if _, err := renderWriteActionJournal(a); err == nil {
				t.Fatal("invalid relation command accepted")
			}
		})
	}
	original, changed := relationUpdateModel(t), relationUpdateModel(t)
	changed.Group.RelationUpdates.Order = []int{1, 0}
	if err := validateGroupRewrite(original, changed); err == nil {
		t.Fatal("changed original order")
	}
}

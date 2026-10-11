package ghstore

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/models"
)

func updateCommentsModel(t *testing.T) ActionJournal {
	t.Helper()
	a := plannedUpdateModelFixture(t)
	g := a.Group
	g.Command, g.UpdateComments = "update-comments", &ActionUpdateComments{}
	for i := range g.Members {
		m := &g.Members[i]
		activity := models.Activity{Kind: "comment", SessionID: a.SessionID, OperationID: fmt.Sprintf("comment-%d", i), Message: "Inline comment"}
		data, body, err := prepareActivityInput(m.IssueID, activity)
		if err != nil {
			t.Fatal(err)
		}
		g.UpdateComments.Inputs = append(g.UpdateComments.Inputs, ActionUpdateActivity{Update: m.Input, Comment: &ActionCommentInput{Body: body, OperationID: data.OperationID}})
		m.Input = nil
	}
	return a
}

func TestUpdateCommentsFormatAndActualPredecessor(t *testing.T) {
	a := updateCommentsModel(t)
	body, err := renderWriteActionJournal(a)
	if err != nil || !strings.Contains(body, actionUpdateCommentsJournalStart) {
		t.Fatal("new command framing", err)
	}
	comment := journalCommentFixture(t, a)
	comment.Body = body
	decoded, err := decodeActionJournal(comment, a.CarrierID)
	if err != nil || !reflect.DeepEqual(decoded.ActionJournal, a) {
		t.Fatal("command round trip", err)
	}
	for _, legacy := range []string{actionPlannedUpdateGroupJournalStart, actionUpdateActivityJournalStart, actionHandoffReviewJournalStart} {
		comment.Body = strings.Replace(body, actionUpdateCommentsJournalStart, legacy, 1)
		if _, err := decodeActionJournal(comment, a.CarrierID); err == nil {
			t.Fatal("legacy format accepted new semantics", legacy)
		}
	}
	m := a.Group.Members[0]
	input := a.Group.UpdateComments.Inputs[0]
	clock := m.Before.UpdatedAt
	root := &ActionJournalRecord{ActionJournal: a, ID: "ghc-100", Author: "author", CreatedAt: clock, UpdatedAt: clock}
	first := &ActionJournalRecord{ActionJournal: ActionJournal{OperationID: "field-attempt", SessionID: a.SessionID, IssueID: m.IssueID, CarrierID: a.CarrierID, Action: "update", Phase: "applied", Before: &m.Before, UpdateIntent: input.Update, GroupOperationID: a.OperationID, GroupContract: updateCommentsContract, GroupSequence: 1}, ID: "ghc-101", Author: root.Author, CreatedAt: clock.Add(time.Second), UpdatedAt: clock.Add(2 * time.Second)}
	after, err := input.Update.Target.contents()
	if err != nil {
		t.Fatal(err)
	}
	after.UpdatedAt = clock.Add(2 * time.Second)
	after.Revision, _ = after.checksum()
	first.After = &after
	second := &ActionJournalRecord{ActionJournal: ActionJournal{OperationID: "comment-attempt", SessionID: a.SessionID, IssueID: m.IssueID, CarrierID: a.CarrierID, Action: "comment_create", Phase: "prepared", Before: first.After, CommentInput: input.Comment, GroupOperationID: a.OperationID, GroupContract: updateCommentsContract, GroupSequence: 2, GroupPreviousReceiptID: first.ID}, ID: "ghc-102", Author: root.Author, CreatedAt: clock.Add(3 * time.Second), UpdatedAt: clock.Add(3 * time.Second)}
	if _, err := orderedGroupEffects(root, []*ActionJournalRecord{first, second}); err != nil {
		t.Fatal("actual predecessor rejected", err)
	}
	memberBody, err := renderWriteActionJournal(second.ActionJournal)
	if err != nil || !strings.Contains(memberBody, actionUpdateCommentsMemberStart) {
		t.Fatal("member framing", err)
	}
	memberComment := journalCommentFixture(t, second.ActionJournal)
	memberComment.Body = memberBody
	if _, err := decodeActionJournal(memberComment, a.CarrierID); err != nil {
		t.Fatal("member round trip", err)
	}
	memberComment.Body = strings.Replace(memberBody, actionUpdateCommentsMemberStart, actionHandoffReviewMemberStart, 1)
	if _, err := decodeActionJournal(memberComment, a.CarrierID); err == nil {
		t.Fatal("legacy member accepted new contract")
	}
	second.Before = &m.Before
	if _, err := orderedGroupEffects(root, []*ActionJournalRecord{first, second}); err == nil {
		t.Fatal("comment accepted an invented predecessor state")
	}
	large := updateCommentsModel(t)
	for i := range large.Group.Members {
		m := &large.Group.Members[i]
		m.Before.Body = strings.Repeat("Large original content\n", 4000)
		m.Before.Revision, _ = m.Before.checksum()
		large.Group.UpdateComments.Inputs[i].Update.Target.Body = m.Before.Body
	}
	body, err = renderWriteActionJournal(large)
	if err != nil || !strings.Contains(body, actionCompressedJournalStart) {
		t.Fatal("compressed new command", err)
	}
	comment.Body = body
	decoded, err = decodeActionJournal(comment, a.CarrierID)
	if err != nil || !reflect.DeepEqual(decoded.ActionJournal, large) {
		t.Fatal("compressed round trip", err)
	}
}

func TestUpdateCommentsRejectsUndeclaredOrReplacedInputs(t *testing.T) {
	for name, mutate := range map[string]func(*ActionJournal){
		"missing comment":   func(a *ActionJournal) { a.Group.UpdateComments.Inputs[1].Comment = nil },
		"native transition": func(a *ActionJournal) { a.Group.UpdateComments.Inputs[0].Update.Target.State = "closed" },
		"repeated operation": func(a *ActionJournal) {
			a.Group.UpdateComments.Inputs[1].Comment = a.Group.UpdateComments.Inputs[0].Comment
		},
		"nested proof": func(a *ActionJournal) {
			a.Group.UpdateComments.Inputs[0].ForwardDigests = []string{strings.Repeat("f", 64)}
		},
		"omitted member input": func(a *ActionJournal) { a.Group.UpdateComments.Inputs = a.Group.UpdateComments.Inputs[:1] },
	} {
		t.Run(name, func(t *testing.T) {
			a := updateCommentsModel(t)
			mutate(&a)
			if _, err := renderWriteActionJournal(a); err == nil {
				t.Fatal("invalid command accepted")
			}
		})
	}
	original := updateCommentsModel(t)
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var changed ActionJournal
	if err := json.Unmarshal(data, &changed); err != nil {
		t.Fatal(err)
	}
	changed.Group.UpdateComments.Inputs[1].Update.Target.Title = "Replaced complete input"
	if err := validateGroupRewrite(original, changed); err == nil {
		t.Fatal("original complete request was rewritten")
	}
}

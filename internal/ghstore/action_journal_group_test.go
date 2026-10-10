package ghstore

import (
	"reflect"
	"strings"
	"testing"
)

func groupModelFixture(t *testing.T) ActionJournal {
	t.Helper()
	state := journalStateFixture(t, "Member")
	return ActionJournal{OperationID: "td-op-group", SessionID: "ses_actor", IssueID: "gh-1", CarrierID: "gh-9", Action: "group", Phase: "prepared", Group: &ActionGroup{Command: "start", Members: []ActionGroupMember{{IssueID: "gh-1", Before: state}, {IssueID: "gh-2", Before: state}}}}
}

func TestGroupJournalStrictMembershipAndLegacyTransport(t *testing.T) {
	a := groupModelFixture(t)
	body, err := renderWriteActionJournal(a)
	if err != nil {
		t.Fatal(err)
	}
	c := journalCommentFixture(t, a)
	c.Body = body
	record, err := decodeActionJournal(c, "gh-9")
	if err != nil || !reflect.DeepEqual(record.ActionJournal, a) || !strings.Contains(body, actionGroupJournalStart) {
		t.Fatal(record, err)
	}
	for name, change := range map[string]func(*ActionJournal){
		"root":           func(a *ActionJournal) { a.IssueID = "gh-3" },
		"duplicate":      func(a *ActionJournal) { a.Group.Members[1].IssueID = "gh-1" },
		"unsupported":    func(a *ActionJournal) { a.Group.Command = "approve" },
		"snapshot":       func(a *ActionJournal) { a.Group.Members[0].Before.Revision = "wrong" },
		"single":         func(a *ActionJournal) { a.Group.Members = a.Group.Members[:1] },
		"result":         func(a *ActionJournal) { a.Phase = "applied" },
		"native carrier": func(a *ActionJournal) { a.CarrierID = "gh-2" },
	} {
		t.Run(name, func(t *testing.T) {
			copy := a
			g := *a.Group
			g.Members = append([]ActionGroupMember{}, g.Members...)
			copy.Group = &g
			change(&copy)
			if _, err := renderWriteActionJournal(copy); err == nil {
				t.Fatal("invalid membership accepted")
			}
		})
	}
	c.Body = strings.Replace(body, actionGroupJournalStart, actionJournalStart, 1)
	if _, err := decodeActionJournal(c, "gh-9"); err == nil {
		t.Fatal("legacy schema accepted group")
	}
	a.Group.Members[0].Before.Body = strings.Repeat("큰 group 기록\n", 4000)
	a.Group.Members[0].Before.Revision, _ = a.Group.Members[0].Before.checksum()
	a.Group.Members[1].Before = a.Group.Members[0].Before
	body, err = renderWriteActionJournal(a)
	if err != nil || !strings.Contains(body, actionCompressedJournalStart) {
		t.Fatal(err)
	}
	c.Body = body
	record, err = decodeActionJournal(c, "gh-9")
	if err != nil || !reflect.DeepEqual(record.ActionJournal, a) {
		t.Fatal("v10 envelope round trip", err)
	}
}

func TestReviewGroupVersionRootsAndImmutableMembership(t *testing.T) {
	a := groupModelFixture(t)
	a.Group.Command = "review"
	a.Group.Roots = []string{"gh-1"}
	body, err := renderWriteActionJournal(a)
	if err != nil || !strings.Contains(body, actionReviewGroupJournalStart) {
		t.Fatal(body, err)
	}
	c := journalCommentFixture(t, a)
	c.Body = body
	decoded, err := decodeActionJournal(c, "gh-9")
	if err != nil || !reflect.DeepEqual(decoded.ActionJournal, a) {
		t.Fatal(decoded, err)
	}
	c.Body = strings.Replace(body, actionReviewGroupJournalStart, actionGroupJournalStart, 1)
	if _, err := decodeActionJournal(c, "gh-9"); err == nil {
		t.Fatal("v10 silently accepted review hierarchy")
	}
	for _, roots := range [][]string{nil, {"gh-1", "gh-1"}, {"gh-3"}, {"gh-2"}} {
		b := a
		g := *a.Group
		g.Roots = roots
		b.Group = &g
		if _, err := renderWriteActionJournal(b); err == nil {
			t.Fatal("invalid roots", roots)
		}
	}
	b := a
	g := *a.Group
	g.Roots = []string{"gh-1", "gh-2"}
	b.Group = &g
	if err := validateGroupRewrite(a, b); err == nil {
		t.Fatal("roots rewritten")
	}
}

func TestNativeGroupVersionCascadePolicyAndInverseCapacity(t *testing.T) {
	a := groupModelFixture(t)
	a.Group.Command = "close"
	a.Group.Roots = []string{"gh-1"}
	a.Group.Cascade = true
	body, err := renderWriteActionJournal(a)
	if err != nil || !strings.Contains(body, actionNativeGroupJournalStart) {
		t.Fatal(body, err)
	}
	c := journalCommentFixture(t, a)
	c.Body = body
	decoded, err := decodeActionJournal(c, "gh-9")
	if err != nil || !reflect.DeepEqual(decoded.ActionJournal, a) {
		t.Fatal(decoded, err)
	}
	c.Body = strings.Replace(body, actionNativeGroupJournalStart, actionReviewGroupJournalStart, 1)
	if _, err := decodeActionJournal(c, "gh-9"); err == nil {
		t.Fatal("legacy group accepted native graph")
	}
	next := a
	g := *a.Group
	g.Cascade = false
	next.Group = &g
	if _, err := renderWriteActionJournal(next); err == nil {
		t.Fatal("close omitted closure graph")
	}
	if err := validateGroupRewrite(a, next); err == nil {
		t.Fatal("cascade role rewritten")
	}
	f, _ := nativeExecutionFixture(t, "close-approved")
	forward, err := decodeActionJournal(*f.comment, "gh-9")
	if err != nil {
		t.Fatal(err)
	}
	after := *forward.After
	projected, err := projectGroupInverseState(a, ActionGroupMember{IssueID: forward.IssueID, Before: *forward.Before}, after)
	if err != nil || projected.Body == forward.Before.Body || !strings.Contains(projected.Body, `"action":"undo_native"`) {
		t.Fatal("native inverse capacity omitted audit", err)
	}
}

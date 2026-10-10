package ghstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func orderedArchiveFixture(t *testing.T) ActionJournal {
	t.Helper()
	group, effects, events, _ := orderedCompensationFixture(t)
	group.Group.Ordered = &ActionOrderedGroup{Events: events}
	for i, effect := range effects {
		effect.GroupSequence = i + 1
		if i > 0 {
			effect.GroupPreviousReceiptID = effects[i-1].ID
		}
		body, err := renderWriteActionJournal(effect.ActionJournal)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256([]byte(body))
		group.Group.Ordered.Forward = append(group.Group.Ordered.Forward, OrderedGroupEffect{Record: effect, BodySHA256: hex.EncodeToString(digest[:]), Status: "applied"})
	}
	group.Phase = "applied"
	group.Group.Forward = &ActionGroupObservation{SessionID: group.SessionID, Actor: group.Author, ObservedAt: time.Now().UTC(), Proof: "verified-group-receipts", Members: []ActionGroupResultMember{{IssueID: "gh-2", Status: "noop", State: group.Group.Members[0].Before}, {IssueID: "gh-1", ReceiptID: effects[1].ID, Status: "applied", State: *effects[1].After}}}
	return group.ActionJournal
}

func orderedArchiveStepFixture(t *testing.T, a *ActionJournal, complete bool) *OrderedGroupUndoStep {
	t.Helper()
	a.Phase, a.UndoOperationID = "undo_prepared", "td-op-group-inverse"
	effects := []*ActionJournalRecord{}
	for _, f := range a.Group.Ordered.Forward {
		if f.Status != "unapplied" {
			effects = append(effects, f.Record)
		}
	}
	source := *effects[len(effects)-1].After
	if len(a.Group.Ordered.Steps) > 0 {
		source = *a.Group.Ordered.Steps[len(a.Group.Ordered.Steps)-1].Result
	}
	events := a.Group.Ordered.Events["gh-1"]
	if len(a.Group.Ordered.Steps) > 0 {
		events = *a.Group.Ordered.Steps[len(a.Group.Ordered.Steps)-1].EventsAfter
	}
	op := "td-op-inverse-first"
	if len(a.Group.Ordered.Steps) > 0 {
		op = "td-op-inverse-second"
	}
	group := &ActionJournalRecord{ActionJournal: *a, ID: "ghc-200", Author: a.Group.Forward.Actor}
	step, err := PlanOrderedGroupUndoStep(group, effects, a.Group.Ordered.Events, a.Group.Ordered.Steps, source, events, a.UndoOperationID, op, a.SessionID, group.Author, time.Now().UTC().Add(time.Hour+time.Duration(len(a.Group.Ordered.Steps))*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if complete {
		completeOrderedStepFixture(t, step, false)
	}
	a.Group.Ordered.Steps = append(a.Group.Ordered.Steps, step)
	return step
}

func TestOrderedGroupArchiveVersionsAndCompletedCompensation(t *testing.T) {
	a := orderedArchiveFixture(t)
	for _, phase := range []string{"applied", "undo_prepared", "undone"} {
		if phase == "undo_prepared" {
			orderedArchiveStepFixture(t, &a, true)
		}
		if phase == "undone" {
			last := orderedArchiveStepFixture(t, &a, true)
			a.Phase = "undone"
			inverse := *a.Group.Forward
			inverse.Members = append([]ActionGroupResultMember{}, inverse.Members...)
			inverse.Members[1].State, inverse.Members[1].Status = *last.Result, "undone"
			a.Group.Inverse = &inverse
		}
		body, err := renderWriteActionJournal(a)
		if err != nil || !strings.Contains(body, actionOrderedGroupJournalStart) {
			t.Fatal(phase, body, err)
		}
		comment := journalCommentFixture(t, a)
		comment.Body = body
		decoded, err := decodeActionJournal(comment, "gh-9")
		if err != nil || !reflect.DeepEqual(decoded.ActionJournal, a) {
			t.Fatal(phase, "ordered archive round trip", err)
		}
		comment.Body = strings.Replace(body, actionOrderedGroupJournalStart, actionNativeGroupJournalStart, 1)
		if _, err := decodeActionJournal(comment, "gh-9"); err == nil {
			t.Fatal("v13 accepted ordered inverse policy")
		}
	}
	member := a.Group.Ordered.Forward[0].Record.ActionJournal
	body, err := renderWriteActionJournal(member)
	if err != nil || !strings.Contains(body, actionOrderedGroupMemberJournalStart) {
		t.Fatal("v15 member", err)
	}
	c := journalCommentFixture(t, member)
	c.Body = strings.Replace(body, actionOrderedGroupMemberJournalStart, actionGroupMemberJournalStart, 1)
	if _, err := decodeActionJournal(c, "gh-9"); err == nil {
		t.Fatal("v11 accepted ordered member fields")
	}
}

func TestOrderedGroupArchiveRejectsBrokenLinksAndPlans(t *testing.T) {
	for _, mode := range []string{"sequence", "predecessor", "digest", "author", "forward-summary", "native-events", "individual-inverse", "pending-successor", "plan", "result", "completion-missing", "observed-promotion"} {
		t.Run(mode, func(t *testing.T) {
			a := orderedArchiveFixture(t)
			switch mode {
			case "sequence":
				a.Group.Ordered.Forward[1].Record.GroupSequence = 3
			case "predecessor":
				a.Group.Ordered.Forward[1].Record.GroupPreviousReceiptID = "ghc-199"
			case "digest":
				a.Group.Ordered.Forward[0].BodySHA256 = "invalid"
			case "author":
				a.Group.Ordered.Forward[0].Record.Author = "foreign"
			case "forward-summary":
				a.Group.Forward.Members[1].ReceiptID = "ghc-201"
			case "native-events":
				a.Group.Ordered.Events["gh-1"][0].Event = "reopened"
			case "individual-inverse":
				a.Group.Ordered.Forward[1].Record.Phase = "undo_prepared"
			case "pending-successor":
				first := orderedArchiveStepFixture(t, &a, false)
				a.Group.Ordered.Steps = append(a.Group.Ordered.Steps, first)
			case "plan":
				step := orderedArchiveStepFixture(t, &a, false)
				step.Input.Body += "foreign edit"
			case "result":
				step := orderedArchiveStepFixture(t, &a, true)
				step.Result.Title = "foreign title"
				step.Result.Revision, _ = step.Result.checksum()
			case "completion-missing":
				orderedArchiveStepFixture(t, &a, true)
				a.Phase = "undone"
				inverse := *a.Group.Forward
				a.Group.Inverse = &inverse
			case "observed-promotion":
				step := orderedArchiveStepFixture(t, &a, true)
				step.Proof, step.ObserverSession, step.ObservedAt = inverseObservationProof, "ses_observer", step.At.Add(time.Second)
				last := orderedArchiveStepFixture(t, &a, true)
				a.Phase = "undone"
				inverse := *a.Group.Forward
				inverse.Members = append([]ActionGroupResultMember{}, inverse.Members...)
				inverse.Members[1].State, inverse.Members[1].Status = *last.Result, "undone"
				a.Group.Inverse = &inverse
			}
			if _, err := renderWriteActionJournal(a); err == nil {
				t.Fatal("invalid ordered history accepted")
			}
		})
	}
}

func TestOrderedGroupArchivePartialForwardAndBoundedTransport(t *testing.T) {
	a := orderedArchiveFixture(t)
	last := &a.Group.Ordered.Forward[1]
	last.Status, last.Record.Phase, last.Record.After = "unapplied", "prepared", nil
	a.Group.Forward.Partial = true
	first := a.Group.Ordered.Forward[0].Record
	a.Group.Forward.Members[1].State, a.Group.Forward.Members[1].Status, a.Group.Forward.Members[1].ReceiptID = *first.After, "applied", first.ID
	a.Group.Ordered.Events["gh-1"] = []nativeStateEvent{}
	if err := a.validate(); err != nil {
		t.Fatal("actual earlier result lost behind unapplied terminal input", err)
	}
	orderedArchiveStepFixture(t, &a, false)
	if err := a.validate(); err != nil {
		t.Fatal("partial original could not plan its earlier inverse", err)
	}
	a = groupModelFixture(t)
	a.Group.Ordered = &ActionOrderedGroup{}
	state := &a.Group.Members[0].Before
	_, meta, _, _ := decodeBody(state.Body)
	state.Body, _ = encodeBody(strings.Repeat("큰 ordered 기록\n", 7000), meta)
	state.Revision, _ = state.checksum()
	a.Group.Members[1].Before = *state
	body, err := renderWriteActionJournal(a)
	if err != nil || !strings.Contains(body, actionCompressedJournalStart) {
		t.Fatal("bounded v14 compressed transport", err)
	}
	c := journalCommentFixture(t, a)
	c.Body = body
	decoded, err := decodeActionJournal(c, "gh-9")
	if err != nil || !reflect.DeepEqual(decoded.ActionJournal, a) {
		t.Fatal("v14 compressed round trip", err)
	}
}

func TestOrderedGroupArchiveRewriteKeepsCompletedProvenance(t *testing.T) {
	a := orderedArchiveFixture(t)
	step := orderedArchiveStepFixture(t, &a, false)
	data, _ := json.Marshal(a)
	var next ActionJournal
	if err := json.Unmarshal(data, &next); err != nil {
		t.Fatal(err)
	}
	completeOrderedStepFixture(t, next.Group.Ordered.Steps[0], true)
	if err := validateGroupRewrite(a, next); err != nil {
		t.Fatal("one receipt-only completion rejected", err)
	}
	if step.Result != nil {
		t.Fatal("original pending intent mutated")
	}
	data, _ = json.Marshal(next)
	var promoted ActionJournal
	_ = json.Unmarshal(data, &promoted)
	promoted.Group.Ordered.Steps[0].Proof = "verified-group-inverse"
	if err := validateGroupRewrite(next, promoted); err == nil {
		t.Fatal("completed observed provenance rewritten")
	}
	promoted = next
	g := *next.Group
	o := *next.Group.Ordered
	g.Ordered, promoted.Group = &o, &g
	o.Events = map[string][]nativeStateEvent{"gh-1": {}}
	if err := validateGroupRewrite(next, promoted); err == nil {
		t.Fatal("original forward events rewritten")
	}
}

func TestOrderedGroupArchivePreservesEmptyActualObservations(t *testing.T) {
	a := groupModelFixture(t)
	a.Phase = "applied"
	a.Group.Ordered = &ActionOrderedGroup{Forward: []OrderedGroupEffect{}, Events: map[string][]nativeStateEvent{}}
	a.Group.Forward = &ActionGroupObservation{SessionID: a.SessionID, Actor: "github-author", ObservedAt: time.Now().UTC(), Proof: "verified-group-receipts", Members: []ActionGroupResultMember{}}
	for _, member := range a.Group.Members {
		a.Group.Forward.Members = append(a.Group.Forward.Members, ActionGroupResultMember{IssueID: member.IssueID, Status: "noop", State: member.Before})
	}
	for _, phase := range []string{"applied", "undone"} {
		if phase == "undone" {
			a.Phase, a.UndoOperationID = phase, "td-op-empty-inverse"
			inverse := *a.Group.Forward
			a.Group.Inverse = &inverse
		}
		body, err := renderWriteActionJournal(a)
		if err != nil {
			t.Fatal(phase, err)
		}
		comment := journalCommentFixture(t, a)
		comment.Body = body
		decoded, err := decodeActionJournal(comment, "gh-9")
		if err != nil || !reflect.DeepEqual(decoded.ActionJournal, a) {
			t.Fatal(phase, "empty observations became absent", err)
		}
	}
}

func TestOrderedGroupUnconnectedExecutorCannotWrite(t *testing.T) {
	a := orderedArchiveFixture(t)
	f := newJournalWriteFixture(t, "")
	group := &ActionJournalRecord{ActionJournal: a, ID: "ghc-200", Author: a.Group.Forward.Actor}
	if _, err := f.client.undoActionGroup(context.Background(), group, a.SessionID); err == nil {
		t.Fatal("incomplete ordered executor accepted a target write")
	}
	if f.targetWrites != 0 || f.receiptWrites != 0 || f.intentWrites != 0 || f.carrierWrites != 0 {
		t.Fatal("incomplete integration reached a writer")
	}
}

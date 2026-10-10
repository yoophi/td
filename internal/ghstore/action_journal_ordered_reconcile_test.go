package ghstore

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func preparedOrderedRecoveryFixture(t *testing.T, tailPrepared, tailApplied bool) *orderedObservationFixture {
	t.Helper()
	a := orderedArchiveFixture(t)
	o := newOrderedObservationFixture(t, a)
	if tailPrepared {
		tail := *a.Group.Ordered.Forward[1].Record
		tail.Phase, tail.After = "prepared", nil
		raw := o.comments[202]
		var err error
		raw.Body, err = renderWriteActionJournal(tail.ActionJournal)
		if err != nil {
			t.Fatal(err)
		}
		o.comments[202] = raw
		if !tailApplied {
			issue := observedFixtureIssue(t, tail.IssueID, *tail.Before)
			o.issues[issue.Number] = issue
			o.events[tail.IssueID] = append([]nativeStateEvent{}, (*tail.UpdateIntent.NativeEvents)...)
		}
	}
	a.Phase, a.Group.Forward, a.Group.Ordered = "prepared", nil, &ActionOrderedGroup{}
	root := o.comments[200]
	var err error
	root.Body, err = renderWriteActionJournal(a)
	if err != nil {
		t.Fatal(err)
	}
	o.comments[200] = root
	o.group, err = decodeActionJournal(root, "gh-9")
	if err != nil {
		t.Fatal(err)
	}
	o.allowReceiptWrites = true
	return o
}

func runtimeOrderedStepFixture(t *testing.T, a *ActionJournal, complete bool) *OrderedGroupUndoStep {
	t.Helper()
	a.Phase, a.UndoOperationID = "undo_prepared", "td-op-runtime-group-inverse"
	effects := []*ActionJournalRecord{}
	for _, effect := range a.Group.Ordered.Forward {
		if effect.Status != "unapplied" {
			effects = append(effects, effect.Record)
		}
	}
	source, events := *effects[len(effects)-1].After, a.Group.Ordered.Events["gh-1"]
	op := "td-op-runtime-first"
	if len(a.Group.Ordered.Steps) > 0 {
		last := a.Group.Ordered.Steps[len(a.Group.Ordered.Steps)-1]
		source, events, op = *last.Result, *last.EventsAfter, "td-op-runtime-second"
	}
	group := &ActionJournalRecord{ActionJournal: *a, ID: "ghc-200", Author: a.Group.Forward.Actor}
	step, err := PlanOrderedGroupUndoStep(group, effects, a.Group.Ordered.Events, a.Group.Ordered.Steps, source, events, a.UndoOperationID, op, a.SessionID, group.Author, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if complete {
		completeRuntimeOrderedStepFixture(t, step)
	}
	a.Group.Ordered.Steps = append(a.Group.Ordered.Steps, step)
	return step
}

func completeRuntimeOrderedStepFixture(t *testing.T, step *OrderedGroupUndoStep) {
	t.Helper()
	completeOrderedStepFixture(t, step, false)
	step.Result.UpdatedAt = time.Now().UTC()
	step.Result.Revision, _ = step.Result.checksum()
}

func TestOrderedGroupForwardCompletionFreezesActualReceipts(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(map[bool]string{false: "successful-callback", true: "unapplied-tail"}[partial], func(t *testing.T) {
			o := preparedOrderedRecoveryFixture(t, partial, false)
			before, _ := json.Marshal(o.issues)
			result, err := o.client.reconcileActionGroup(context.Background(), o.group, o.group.SessionID, o.group.Author, true, !partial)
			if err != nil || !result.GroupReceiptSaved || result.TargetWriteReplayed || result.Journal.Phase != "applied" {
				t.Fatal(result, err)
			}
			g := result.Journal.Group
			if len(g.Ordered.Forward) != 2 || g.Forward.Partial != partial || g.Forward.Members[1].ReceiptID != map[bool]string{false: "ghc-202", true: "ghc-201"}[partial] {
				t.Fatal("actual ordered outcome lost", g)
			}
			if o.receiptWrites != 1 {
				t.Fatal("expected only one root receipt write", o.receiptWrites)
			}
			after, _ := json.Marshal(o.issues)
			if string(before) != string(after) {
				t.Fatal("receipt completion changed a target")
			}
			if _, err := o.client.reconcileActionGroup(context.Background(), result.Journal, o.group.SessionID, o.group.Author, true, false); err != nil || o.receiptWrites != 1 {
				t.Fatal("repeated completed confirmation wrote again", err, o.receiptWrites)
			}
		})
	}
}

func TestOrderedGroupForwardRecoveryKeepsOriginalIdentityAndWeakProof(t *testing.T) {
	o := preparedOrderedRecoveryFixture(t, true, true)
	before, _ := json.Marshal(o.issues)
	result, err := o.client.reconcileActionGroup(context.Background(), o.group, "ses_recovery", o.group.Author, true, false)
	if err != nil || !result.GroupReceiptSaved || result.Journal.OperationID != o.group.OperationID || result.Journal.SessionID != o.group.SessionID {
		t.Fatal(result, err)
	}
	effect := result.Journal.Group.Ordered.Forward[1]
	if effect.Status != "observed" || effect.Record.UpdateObservation.SessionID != "ses_recovery" || effect.Record.SessionID != o.group.SessionID || result.Journal.Group.Forward.Proof != "group-contents-observed" || o.receiptWrites != 2 {
		t.Fatal("original identity or observed proof changed", result, o.receiptWrites)
	}
	after, _ := json.Marshal(o.issues)
	if string(before) != string(after) {
		t.Fatal("forward recovery replayed a target")
	}
}

func TestOrderedGroupInverseReceiptRecoveryNeverReplays(t *testing.T) {
	for _, applied := range []bool{false, true} {
		t.Run(map[bool]string{false: "unapplied", true: "applied"}[applied], func(t *testing.T) {
			a := orderedArchiveFixture(t)
			pending := runtimeOrderedStepFixture(t, &a, false)
			o := newOrderedObservationFixture(t, a)
			o.allowReceiptWrites = true
			if applied {
				actual := *pending
				completeRuntimeOrderedStepFixture(t, &actual)
				o.issues[1] = observedFixtureIssue(t, "gh-1", *actual.Result)
				o.events["gh-1"] = *actual.EventsAfter
			}
			before, _ := json.Marshal(o.issues)
			result, err := o.client.reconcileActionGroup(context.Background(), o.group, "ses_recovery", o.group.Author, true, false)
			if !applied {
				if err == nil || o.receiptWrites != 0 {
					t.Fatal("unapplied inverse replayed", result, err, o.receiptWrites)
				}
				return
			}
			if err != nil || !result.GroupReceiptSaved || result.Journal.Phase != "undo_prepared" || o.receiptWrites != 1 {
				t.Fatal(result, err, o.receiptWrites)
			}
			step := result.Journal.Group.Ordered.Steps[0]
			if step.Proof != inverseObservationProof || step.ObserverSession != "ses_recovery" || !sameOrderedStepIntent(step, pending) || pending.Result != nil || result.TargetWriteReplayed {
				t.Fatal("pending identity or weak proof changed", step)
			}
			after, _ := json.Marshal(o.issues)
			if string(before) != string(after) {
				t.Fatal("inverse recovery changed target")
			}
			if _, err := o.client.reconcileActionGroup(context.Background(), result.Journal, "ses_recovery", o.group.Author, true, false); err != nil || o.receiptWrites != 1 {
				t.Fatal("partial confirmation replayed", err, o.receiptWrites)
			}
		})
	}
}

func TestOrderedGroupFinalRecoveryPreservesObservedProof(t *testing.T) {
	a := orderedArchiveFixture(t)
	runtimeOrderedStepFixture(t, &a, true)
	pending := runtimeOrderedStepFixture(t, &a, false)
	o := newOrderedObservationFixture(t, a)
	o.allowReceiptWrites = true
	actual := *pending
	completeRuntimeOrderedStepFixture(t, &actual)
	o.issues[1], o.events["gh-1"] = observedFixtureIssue(t, "gh-1", *actual.Result), *actual.EventsAfter
	original := slicesOfReceiptBodies(o.comments)
	result, err := o.client.reconcileActionGroup(context.Background(), o.group, "ses_recovery", o.group.Author, true, true)
	if err != nil || result.Journal.Phase != "undone" || result.Journal.Group.Inverse.Proof != "group-contents-observed" || o.receiptWrites != 2 {
		t.Fatal(result, err, o.receiptWrites)
	}
	if !reflect.DeepEqual(original, slicesOfReceiptBodies(o.comments)) {
		t.Fatal("inverse recovery rewrote original forward receipts")
	}
	if _, err := o.client.reconcileActionGroup(context.Background(), result.Journal, "ses_recovery", o.group.Author, true, true); err != nil || o.receiptWrites != 2 {
		t.Fatal("completed inverse confirmation wrote again", err)
	}
}

func slicesOfReceiptBodies(comments map[int64]apiComment) map[int64]string {
	out := map[int64]string{}
	for id, comment := range comments {
		if id != 200 {
			out[id] = comment.Body
		}
	}
	return out
}

func TestOrderedGroupRecoveryFencesWatchedChangesBeforeReceiptPatch(t *testing.T) {
	for _, pending := range []bool{false, true} {
		o := preparedOrderedRecoveryFixture(t, pending, true)
		base := o.client.run
		o.client.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
			if args[4] == "GET" && strings.HasSuffix(args[5], "/issues/comments/201") {
				issue := o.issues[2]
				issue.Title += " foreign"
				o.issues[2] = issue
			}
			return base(ctx, dir, payload, args...)
		}
		if _, err := o.client.reconcileActionGroup(context.Background(), o.group, "ses_recovery", o.group.Author, true, false); err == nil || o.receiptWrites != 0 {
			t.Fatal("watched conflict passed receipt fence", err, o.receiptWrites)
		}
	}
}

func TestOrderedGroupLostCompletionReplyCanBeConfirmedWithoutReplay(t *testing.T) {
	o := preparedOrderedRecoveryFixture(t, false, true)
	o.failReceiptAfterWrite = true
	if _, err := o.client.reconcileActionGroup(context.Background(), o.group, o.group.SessionID, o.group.Author, true, true); err == nil || o.receiptWrites != 1 {
		t.Fatal("lost completion reply not surfaced", err, o.receiptWrites)
	}
	o.failReceiptAfterWrite = false
	actual, err := decodeActionJournal(o.comments[200], "gh-9")
	if err != nil {
		t.Fatal(err)
	}
	result, err := o.client.reconcileActionGroup(context.Background(), actual, o.group.SessionID, o.group.Author, true, false)
	if err != nil || !result.AlreadyReconciled || result.Applied || o.receiptWrites != 1 {
		t.Fatal("completed original receipt was replayed", result, err, o.receiptWrites)
	}
}

func TestOrderedGroupPublicForwardRecoveryAfterLostMemberReply(t *testing.T) {
	o := preparedOrderedRecoveryFixture(t, true, true)
	o.failReceiptAfterWrite = true
	if _, err := o.client.ReconcileAction(context.Background(), o.group.ID, "ses_recovery", true); err == nil || o.receiptWrites != 1 {
		t.Fatal("lost member receipt response was not surfaced", err, o.receiptWrites)
	}
	o.failReceiptAfterWrite = false
	result, err := o.client.ReconcileAction(context.Background(), o.group.ID, "ses_recovery", true)
	if err != nil || result.Journal.Phase != "applied" || result.TargetWriteReplayed || o.receiptWrites != 2 {
		t.Fatal("public recovery did not continue without member replay", result, err, o.receiptWrites)
	}
	if result.Journal.Group.Ordered.Forward[1].Status != "observed" || result.ForwardOutcome.TargetWriteVerified {
		t.Fatal("public recovery promoted uncertain forward proof", result)
	}
}

func TestOrderedGroupPublicRecoveryRequiresAuthenticatedAuthor(t *testing.T) {
	o := preparedOrderedRecoveryFixture(t, false, true)
	base := o.client.run
	o.client.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		if args[4] == "GET" && args[5] == "user" {
			return json.Marshal(map[string]string{"login": "foreign"})
		}
		return base(ctx, dir, payload, args...)
	}
	if _, err := o.client.ReconcileAction(context.Background(), o.group.ID, "ses_recovery", true); err == nil || o.receiptWrites != 0 {
		t.Fatal("foreign authenticated author could recover a receipt", err, o.receiptWrites)
	}
}

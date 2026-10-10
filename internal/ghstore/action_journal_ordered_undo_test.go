package ghstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestOrderedGroupUndoCompensatesRepeatedTargetInReverse(t *testing.T) {
	o := newOrderedObservationFixture(t, orderedArchiveFixture(t))
	o.allowReceiptWrites, o.allowTargetWrites = true, true
	original := slicesOfReceiptBodies(o.comments)
	result, err := o.client.UndoAction(context.Background(), o.group.ID, o.group.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Journal.Phase != "undone" || o.targetWrites != 2 || o.receiptWrites != 6 || !result.UndoOutcome.TargetWriteVerified {
		t.Fatal(result, o.targetWrites, o.receiptWrites)
	}
	steps := result.Journal.Group.Ordered.Steps
	if steps[0].ForwardReceiptID != "ghc-202" || steps[1].ForwardReceiptID != "ghc-201" || steps[0].Result.State != "open" || steps[1].Source.Revision != steps[0].Result.Revision {
		t.Fatal("actual native compensation did not precede scalar compensation", steps)
	}
	_, details, _, err := decodeBody(o.issues[1].Body)
	if err != nil || details.Details.Status != "blocked" || len(details.Details.Transitions) != 4 {
		t.Fatal("former fields or forward/inverse audit lost", details, err)
	}
	if !reflect.DeepEqual(original, slicesOfReceiptBodies(o.comments)) {
		t.Fatal("ordered undo rewrote original receipts")
	}
	confirmed, err := o.client.UndoAction(context.Background(), o.group.ID, o.group.SessionID)
	if err != nil || !confirmed.AlreadyUndone || o.targetWrites != 2 || o.receiptWrites != 6 {
		t.Fatal("completed ordered undo replayed", confirmed, err)
	}
}

func TestOrderedGroupUndoRecoversUncertainInverseAndContinuesWithoutReplay(t *testing.T) {
	for _, mode := range []string{"target-reply-lost", "receipt-denied"} {
		t.Run(mode, func(t *testing.T) {
			o := newOrderedObservationFixture(t, orderedArchiveFixture(t))
			o.allowReceiptWrites, o.allowTargetWrites = true, true
			if mode == "target-reply-lost" {
				o.failTargetAfterWriteAt = 1
			} else {
				o.failReceiptWriteAt = 3
			}
			if _, err := o.client.UndoAction(context.Background(), o.group.ID, o.group.SessionID); err == nil || o.targetWrites != 1 {
				t.Fatal("uncertain inverse not surfaced", err, o.targetWrites)
			}
			pending, err := decodeActionJournal(o.comments[200], "gh-9")
			if err != nil || pending.Phase != "undo_prepared" || pending.Group.Ordered.Steps[0].Result != nil {
				t.Fatal(pending, err)
			}
			undo, op := pending.UndoOperationID, pending.Group.Ordered.Steps[0].OperationID
			if _, err := o.client.UndoAction(context.Background(), o.group.ID, o.group.SessionID); err == nil || o.targetWrites != 1 {
				t.Fatal("pending inverse replayed without explicit reconcile", err)
			}
			o.failTargetAfterWriteAt, o.failReceiptWriteAt = 0, 0
			if _, err := o.client.ReconcileAction(context.Background(), o.group.ID, "ses_recovery", true); err != nil || o.targetWrites != 1 {
				t.Fatal("receipt-only recovery changed a target", err)
			}
			result, err := o.client.UndoAction(context.Background(), o.group.ID, o.group.SessionID)
			if err != nil || result.Journal.Phase != "undone" || o.targetWrites != 2 {
				t.Fatal("continuation replayed the first effect", result, err, o.targetWrites)
			}
			steps := result.Journal.Group.Ordered.Steps
			if result.Journal.UndoOperationID != undo || steps[0].OperationID != op || steps[0].Proof != inverseObservationProof || steps[0].ObserverSession != "ses_recovery" || steps[1].Proof != "verified-group-inverse" || result.UndoOutcome.TargetWriteVerified || result.UndoOutcome.Proof != "group-contents-observed" {
				t.Fatal("original identity or mixed proof changed", result)
			}
		})
	}
}

func TestOrderedGroupUndoFencesChangesAfterSavedInputBeforeFirstTargetWrite(t *testing.T) {
	for _, mode := range []string{"watched-target", "native-events", "original-clock"} {
		t.Run(mode, func(t *testing.T) {
			o := newOrderedObservationFixture(t, orderedArchiveFixture(t))
			o.allowReceiptWrites, o.allowTargetWrites = true, true
			base := o.client.run
			changed := false
			o.client.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
				if !changed && o.receiptWrites >= 2 && args[4] == "GET" && strings.HasSuffix(args[5], "/issues/comments/201") {
					changed = true
					switch mode {
					case "watched-target":
						issue := o.issues[2]
						issue.Title += " foreign"
						o.issues[2] = issue
					case "native-events":
						o.events["gh-1"] = append(o.events["gh-1"], nativeStateEvent{ID: 99999, Event: "reopened"})
					case "original-clock":
						comment := o.comments[201]
						comment.UpdatedAt = comment.UpdatedAt.Add(1)
						o.comments[201] = comment
					}
				}
				return base(ctx, dir, payload, args...)
			}
			if _, err := o.client.UndoAction(context.Background(), o.group.ID, o.group.SessionID); err == nil || !changed || o.targetWrites != 0 {
				t.Fatal("changed proof passed the saved-input fence", err, changed, o.targetWrites)
			}
		})
	}
}

func TestOrderedGroupUndoRejectsForeignSessionAndKeepsSizingPrivate(t *testing.T) {
	o := newOrderedObservationFixture(t, orderedArchiveFixture(t))
	o.allowReceiptWrites, o.allowTargetWrites = true, true
	if _, err := o.client.UndoAction(context.Background(), o.group.ID, "ses_foreign"); err == nil || o.receiptWrites != 0 || o.targetWrites != 0 {
		t.Fatal("foreign inverse session wrote", err)
	}
	observed, err := o.client.observeOrderedGroup(context.Background(), o.group, false)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(o.group)
	if err := checkOrderedInverseCapacity(o.group, observed, "td-op-sizing-check"); err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(o.group)
	if string(before) != string(after) || o.receiptWrites != 0 || o.targetWrites != 0 {
		t.Fatal("sizing changed observed original evidence")
	}
}

func TestOrderedGroupUndoKeepsHistoricalHandoffFenceAndInvalidatesApproval(t *testing.T) {
	for _, changeAfter := range []int{0, 2, 3} {
		t.Run(map[int]string{0: "valid", 2: "handoff-changed-after-input", 3: "handoff-changed-before-final-receipt"}[changeAfter], func(t *testing.T) {
			a := orderedArchiveFixture(t)
			member, _ := nativeUndoPlanFixture(t, "close-approved")
			member.ID, member.GroupOperationID, member.GroupSequence = "ghc-201", a.OperationID, 1
			effect, err := orderedEffectSnapshot(member, "applied")
			if err != nil {
				t.Fatal(err)
			}
			noop := a.Group.Members[0].Before
			a.IssueID = "gh-1"
			a.Group.Roots = []string{"gh-1", "gh-2"}
			a.Group.Members = []ActionGroupMember{{IssueID: "gh-1", Before: *member.Before}, {IssueID: "gh-2", Before: noop}}
			a.Group.Forward.Members = []ActionGroupResultMember{{IssueID: "gh-1", ReceiptID: member.ID, Status: "applied", State: *member.After}, {IssueID: "gh-2", Status: "noop", State: noop}}
			a.Group.Ordered.Forward = []OrderedGroupEffect{effect}
			events := append([]nativeStateEvent{}, (*member.UpdateIntent.NativeEvents)...)
			events = append(events, nativeStateEvent{ID: 10, Event: "closed"})
			a.Group.Ordered.Events = map[string][]nativeStateEvent{"gh-1": events}
			o := newOrderedObservationFixture(t, a)
			o.allowReceiptWrites, o.allowTargetWrites = true, true
			before, _ := actionStateRecord(member.Before, member.IssueID)
			details, _ := before.CopyDetails()
			if details.ReviewHandoffID != "ghc-77" || details.ReviewBasis == "" {
				t.Fatal("fixture lacks its original handoff proof")
			}
			body, err := renderActivity(activityData{Kind: "handoff", SessionID: "ses_fixture_worker", OperationID: "fixture-handoff", Done: []string{"Synthetic fixture"}})
			if err != nil {
				t.Fatal(err)
			}
			handoff := apiComment{ID: 77, Body: body, UpdatedAt: details.ReviewHandoffUpdatedAt, CreatedAt: details.ReviewHandoffUpdatedAt, IssueURL: "https://api.github.com/repos/owner/repo/issues/1"}
			handoff.User.Login = member.Author
			base := o.client.run
			o.client.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
				if args[4] == "GET" && strings.Contains(args[5], "/issues/1/comments?") {
					actual := handoff
					if changeAfter > 0 && o.receiptWrites >= changeAfter {
						actual.UpdatedAt = actual.UpdatedAt.Add(time.Second)
					}
					return json.Marshal([][]apiComment{{actual}})
				}
				return base(ctx, dir, payload, args...)
			}
			result, err := o.client.UndoAction(context.Background(), o.group.ID, o.group.SessionID)
			if changeAfter > 0 {
				if err == nil || o.targetWrites != map[int]int{2: 0, 3: 1}[changeAfter] || o.receiptWrites != changeAfter {
					t.Fatal("historical handoff change passed saved-input fence", result, err, o.targetWrites, o.receiptWrites)
				}
				return
			}
			if err != nil || o.targetWrites != 1 || result.Journal.Phase != "undone" {
				t.Fatal(result, err, o.targetWrites)
			}
			_, meta, _, _ := decodeBody(o.issues[1].Body)
			if meta.Details.ReviewBasis != "" || meta.Details.ReviewerSession != "" || meta.Details.ReviewHandoffID != "" || activeApproval(meta.Details) != nil {
				t.Fatal("old approval or checkpoint revived", meta)
			}
		})
	}
}

func TestOrderedGroupUndoRejectsOversizedCompletionBeforeAnyWrite(t *testing.T) {
	a := orderedArchiveFixture(t)
	var noise strings.Builder
	for i := 0; i < 625; i++ {
		digest := sha256.Sum256([]byte(fmt.Sprintf("synthetic sizing payload %d", i)))
		noise.WriteString(hex.EncodeToString(digest[:]))
	}
	body, err := encodeBody(noise.String(), metadata{Type: "task", Priority: "P2", Details: &IssueDetails{Status: "closed"}})
	if err != nil {
		t.Fatal(err)
	}
	noop := journalStateFixture(t, "Closed noop")
	noop.Body, noop.State = body, "closed"
	noop.Revision, _ = noop.checksum()
	for i := 3; i <= 48; i++ {
		if i == 9 {
			continue // the separate journal carrier is not a task member
		}
		id := fmt.Sprintf("gh-%d", i)
		a.Group.Roots = append(a.Group.Roots, id)
		a.Group.Members = append(a.Group.Members, ActionGroupMember{IssueID: id, Before: noop})
		a.Group.Forward.Members = append(a.Group.Forward.Members, ActionGroupResultMember{IssueID: id, Status: "noop", State: noop})
	}
	o := newOrderedObservationFixture(t, a)
	o.allowReceiptWrites, o.allowTargetWrites = true, true
	if !strings.Contains(o.comments[200].Body, actionCompressedJournalStart) {
		t.Fatal("fixture must fit as a compressed original group")
	}
	if _, err := o.client.UndoAction(context.Background(), o.group.ID, o.group.SessionID); err == nil || !strings.Contains(err.Error(), "budget") || o.targetWrites != 0 || o.receiptWrites != 0 {
		t.Fatal("oversized eventual inverse was not rejected before writes", err, o.targetWrites, o.receiptWrites)
	}
}

func TestOrderedGroupHistoryCollapsesWholeSequenceAndRejectsChangedEvidence(t *testing.T) {
	for _, mode := range []string{"valid", "missing", "clock", "sequence", "unverified-root"} {
		t.Run(mode, func(t *testing.T) {
			o := newOrderedObservationFixture(t, orderedArchiveFixture(t))
			actions := []ActionJournalRecord{*o.group}
			for _, effect := range o.group.Group.Ordered.Forward {
				actions = append(actions, *effect.Record)
			}
			switch mode {
			case "missing":
				actions = actions[:2]
			case "clock":
				actions[1].UpdatedAt = actions[1].UpdatedAt.Add(time.Second)
			case "sequence":
				actions[2].GroupSequence = 1
			case "unverified-root":
				group := *actions[0].Group
				group.Forward = nil
				actions[0].Group = &group
			}
			collapsed, err := CollapseActionGroups(actions)
			if mode == "valid" {
				if err != nil || len(collapsed) != 1 || collapsed[0].ID != o.group.ID {
					t.Fatal("whole ordered command was not collapsed", collapsed, err)
				}
			} else if err == nil {
				t.Fatal("changed ordered history accepted", mode)
			}
		})
	}
}

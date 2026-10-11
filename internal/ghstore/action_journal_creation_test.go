package ghstore

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func creationRootFixture(t *testing.T, allocated bool) (*ActionJournalRecord, *ActionJournalRecord) {
	t.Helper()
	f, plan, created := creationBindingFixture(t)
	clock := created.UpdatedAt
	root := &ActionJournalRecord{ActionJournal: ActionJournal{OperationID: plan.create.operationID, SessionID: f.client.journal.SessionID, CarrierID: "gh-9", Action: "create", Phase: "prepared", CreateInput: &plan.create.input, CreationCommand: creationCommandFromPlan(plan)}, ID: "ghc-100", Author: "github-author", CreatedAt: clock, UpdatedAt: clock}
	if !allocated {
		return root, nil
	}
	state, err := actionIssueState(created)
	if err != nil {
		t.Fatal(err)
	}
	root.IssueID, root.After = created.ID, &state
	root.CreationCommand.Allocation = &CreationObservation{Proof: "verified", SessionID: root.SessionID, Actor: root.Author, ObservedAt: clock}
	bound, err := f.client.prepareCreatedRelations(context.Background(), plan, created)
	if err != nil {
		t.Fatal(err)
	}
	bound.root.CarrierID, bound.root.CreationOwner = root.CarrierID, root.OperationID
	child := &ActionJournalRecord{ActionJournal: bound.root, ID: "ghc-101", Author: root.Author, CreatedAt: clock.Add(time.Second), UpdatedAt: clock.Add(time.Second)}
	return root, child
}

func TestCreationCommandFormatsAndOwnership(t *testing.T) {
	for _, allocated := range []bool{false, true} {
		root, child := creationRootFixture(t, allocated)
		for _, record := range []*ActionJournalRecord{root, child} {
			if record == nil {
				continue
			}
			start := actionCreationCommandStart
			if record.CreationOwner != "" {
				start = actionCreationRelationsStart
			}
			body, err := renderWriteActionJournal(record.ActionJournal)
			if err != nil || !strings.Contains(body, start) {
				t.Fatal("new framing", err)
			}
			comment := journalCommentFixture(t, record.ActionJournal)
			comment.Body = body
			decoded, err := decodeActionJournal(comment, record.CarrierID)
			if err != nil || !reflect.DeepEqual(decoded.ActionJournal, record.ActionJournal) {
				t.Fatal("round trip", err)
			}
			for _, old := range []string{actionJournalStart, actionUpdateRelationsJournalStart} {
				comment.Body = strings.Replace(body, start, old, 1)
				if _, err := decodeActionJournal(comment, record.CarrierID); err == nil {
					t.Fatal("legacy marker accepted creation contract")
				}
			}
		}
		if child != nil {
			if err := validateCreationChild(root, child); err != nil {
				t.Fatal(err)
			}
			if err := checkCreationChildAccess(context.Background(), child); err == nil {
				t.Fatal("standalone child undo/recovery allowed")
			}
			rows, err := CollapseActionGroups([]ActionJournalRecord{*child, *root})
			if err != nil || len(rows) != 1 || rows[0].ID != root.ID {
				t.Fatal("creation history did not collapse", err)
			}
			if _, err := CollapseActionGroups([]ActionJournalRecord{*child}); err == nil {
				t.Fatal("orphan child appeared as ordinary history")
			}
		}
	}
}

func TestCreationCommandRejectsChangedInputsAndFabricatedResults(t *testing.T) {
	for _, mode := range []string{"collision", "missing-watch", "missing-event", "same-operation", "input-deps", "actor", "future-result", "unknown-child", "changed-child-input", "child-original"} {
		t.Run(mode, func(t *testing.T) {
			root, child := creationRootFixture(t, true)
			wantChild := false
			switch mode {
			case "collision":
				root.CreationCommand.OriginalIDs = append(root.CreationCommand.OriginalIDs, root.IssueID)
			case "missing-watch":
				root.CreationCommand.Watches = root.CreationCommand.Watches[1:]
			case "missing-event":
				root.CreationCommand.Events = nil
			case "same-operation":
				root.CreationCommand.RelationOperationID = root.OperationID
			case "input-deps":
				root.CreationCommand.DependsOn = []string{}
			case "actor":
				root.CreationCommand.Allocation.Actor = "other-author"
			case "future-result":
				root.CreationCommand.Forward = root.CreationCommand.Allocation
			case "unknown-child":
				child.OperationID = "other-operation"
				wantChild = true
			case "changed-child-input":
				child.Group.RelationUpdates.Inputs[1].Update.Target.Title = "Undeclared field change"
				wantChild = true
			case "child-original":
				child.Group.Members[1].Before.Title = "Replaced baseline"
				wantChild = true
			}
			if wantChild {
				if err := validateCreationChild(root, child); err == nil {
					t.Fatal("changed child accepted")
				}
				return
			}
			if mode == "actor" {
				comment := journalCommentFixture(t, root.ActionJournal)
				if _, err := decodeActionJournal(comment, root.CarrierID); err == nil {
					t.Fatal("foreign actor accepted")
				}
				return
			}
			if err := root.validate(); err == nil {
				t.Fatal("invalid creation accepted")
			}
		})
	}
}

func TestCreationCommandCompressedTransport(t *testing.T) {
	root, _ := creationRootFixture(t, false)
	root.CreationCommand.Watches[0].State.Body = strings.Repeat("Original native description. ", 3000)
	s := &root.CreationCommand.Watches[0].State
	s.Revision, _ = s.checksum()
	body, err := renderWriteActionJournal(root.ActionJournal)
	if err != nil || !strings.Contains(body, actionCompressedJournalStart) {
		t.Fatal("compressed framing", err)
	}
	comment := journalCommentFixture(t, root.ActionJournal)
	comment.Body = body
	decoded, err := decodeActionJournal(comment, root.CarrierID)
	if err != nil || !reflect.DeepEqual(decoded.ActionJournal, root.ActionJournal) {
		t.Fatal("compressed creation round trip", err)
	}
}

func TestCreationChildCannotExecuteWithoutItsParent(t *testing.T) {
	f, original, created := creationBindingFixture(t)
	command, err := f.client.prepareCreatedRelations(context.Background(), original, created)
	if err != nil {
		t.Fatal(err)
	}
	command.root.CreationOwner = original.create.operationID
	calls := 0
	f.client.run = func(context.Context, string, []byte, ...string) ([]byte, error) {
		calls++
		return nil, fmt.Errorf("unexpected API request")
	}
	if _, err := f.client.writeRelationCommand(context.Background(), command); err == nil || calls != 0 {
		t.Fatal("child execution escaped its parent", err, calls)
	}
}

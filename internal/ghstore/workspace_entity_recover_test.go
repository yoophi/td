package ghstore

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func workspaceRecoveryFixture(t *testing.T, kind, mode string) (*journalWriteFixture, MutationRequest) {
	t.Helper()
	f := newJournalWriteFixture(t, mode)
	if err := f.client.TrackActions("ses_actor"); err != nil {
		t.Fatal(err)
	}
	op := "td-workspace-recover-" + kind
	var err error
	if kind == "note" {
		_, err = f.client.CreateNoteWithOptions(context.Background(), "note", "body", "ses_actor", NoteCreateOptions{OperationID: op})
	} else {
		_, err = f.client.CreateBoardWithOptions(context.Background(), "board", "", "ses_actor", BoardCreateOptions{OperationID: op})
	}
	if (mode == "") != (err == nil) {
		t.Fatalf("unexpected creation result: %v", err)
	}
	f.mode = ""
	snapshot, err := f.client.ReadSnapshot(WithFreshSnapshot(context.Background()), true)
	if err != nil {
		t.Fatal(err)
	}
	history, err := snapshot.ActionHistory("")
	if err != nil || len(history) != 1 || history[0].CreateInput == nil {
		t.Fatal(history, err)
	}
	input := history[0].CreateInput
	payload, err := json.Marshal(map[string]any{"title": input.Title, "body": input.Body})
	if err != nil {
		t.Fatal(err)
	}
	return f, mutationRequest("POST", "repos/"+f.client.repo+"/issues", payload)
}

func TestWorkspaceEntityRecoveryObservesBuiltinPatchWithoutReplay(t *testing.T) {
	for _, mode := range []string{"target-uncertain", "receipt-denied", "target-denied"} {
		t.Run(mode, func(t *testing.T) {
			f, virtual := transferBuiltinFixture(t)
			before, err := f.client.ApplyBuiltinBoardTransferObserved(context.Background(), virtual, "ses_actor", BoardCreateOptions{OperationID: "td-initial", ViewMode: "swimlanes"})
			if err != nil {
				t.Fatal(err)
			}
			issue := f.issues[1]
			issue.Number = 3
			f.issues[3] = issue
			f.targetNumber, f.mode = 2, mode
			_, err = f.client.ApplyBuiltinBoardTransferObserved(context.Background(), before, "ses_actor", BoardCreateOptions{OperationID: "td-workspace-update-recovery", Positions: []BoardPosition{{IssueID: "gh-3", Position: 7, AddedAt: time.Now().UTC()}}})
			if err == nil {
				t.Fatal("fixture did not fail")
			}
			f.mode = ""
			root, err := decodeActionJournal(*f.comment, "gh-9")
			if err != nil {
				t.Fatal(err)
			}
			payload, err := json.Marshal(map[string]any{"title": root.UpdateIntent.Target.Title, "body": root.UpdateIntent.Target.Body})
			if err != nil {
				t.Fatal(err)
			}
			request := mutationRequest("PATCH", "repos/"+f.client.repo+"/issues/2", payload)
			writes := f.receiptWrites
			result, err := f.client.RecoverEntityOperation(context.Background(), request)
			if mode == "target-denied" {
				if err == nil || result != nil || f.receiptWrites != writes {
					t.Fatal("unapplied target accepted", result, err)
				}
			} else {
				if err != nil || result.ID != "bd-all-issues" || result.Proof != "observed" || f.receiptWrites != writes+1 {
					t.Fatal(result, err, f.receiptWrites)
				}
				if _, err := f.client.VerifiedEntityOperationMappings(context.Background(), map[string]string{request.OperationID: "board"}); err == nil {
					t.Fatal("observed update promoted to verified")
				}
				if _, err := f.client.RecoverEntityOperation(context.Background(), request); err != nil || f.receiptWrites != writes+1 {
					t.Fatal("repeat rewrote receipt", err)
				}
			}
			if f.targetWrites != 1 || f.creationWrites != 1 {
				t.Fatal("target mutation replayed", f.targetWrites, f.creationWrites)
			}
		})
	}
}

func TestWorkspaceEntityRecoveryObservesLostResponseWithoutReplay(t *testing.T) {
	for _, kind := range []string{"note", "board"} {
		t.Run(kind, func(t *testing.T) {
			f, request := workspaceRecoveryFixture(t, kind, "create-uncertain")
			result, err := f.client.RecoverEntityOperation(context.Background(), request)
			if err != nil || result == nil || result.ID == "" || result.Proof != "observed" || result.JournalID == "" {
				t.Fatal(result, err)
			}
			if f.creationWrites != 1 || f.targetWrites != 0 || f.receiptWrites != 1 {
				t.Fatal("recovery replayed target write", f.creationWrites, f.targetWrites, f.receiptWrites)
			}
			if _, err := f.client.VerifiedEntityOperationMappings(context.Background(), map[string]string{request.OperationID: kind}); err == nil {
				t.Fatal("observation promoted to verified original response")
			}
			repeat, err := f.client.RecoverEntityOperation(context.Background(), request)
			if err != nil || *repeat != *result || f.creationWrites != 1 || f.receiptWrites != 1 {
				t.Fatal("repeat recovery changed result or wrote again", repeat, err)
			}
		})
	}
}

func TestWorkspaceEntityRecoveryKeepsVerifiedProof(t *testing.T) {
	f, request := workspaceRecoveryFixture(t, "note", "")
	result, err := f.client.RecoverEntityOperation(context.Background(), request)
	if err != nil || result.Proof != "verified" || f.creationWrites != 1 || f.receiptWrites != 1 {
		t.Fatal(result, err)
	}
}

func TestWorkspaceEntityRecoveryRejectsChangedIntentActorOrTarget(t *testing.T) {
	for _, mode := range []string{"hash", "path", "kind", "operation", "session", "author", "absent", "changed", "creator"} {
		t.Run(mode, func(t *testing.T) {
			f, request := workspaceRecoveryFixture(t, "note", "create-uncertain")
			switch mode {
			case "hash":
				request.PayloadSHA256 = strings.Repeat("0", 64)
			case "path":
				request.Path += "/other"
			case "kind":
				request.EntityKind = "board"
			case "operation":
				request.OperationID += "-other"
			case "session":
				f.client.journal.SessionID = "ses_other"
			case "author":
				f.mode = "undo-actor"
			case "absent":
				delete(f.issues, 2)
			case "changed", "creator":
				issue := f.issues[2]
				if mode == "changed" {
					issue.Title = "external edit"
				} else {
					issue.User.Login = "someone-else"
				}
				f.issues[2] = issue
			}
			if result, err := f.client.RecoverEntityOperation(context.Background(), request); err == nil || result != nil {
				t.Fatal("unsafe recovery succeeded", result, err)
			}
			if f.creationWrites != 1 || f.targetWrites != 0 || f.receiptWrites != 0 {
				t.Fatal("rejected recovery wrote", f.creationWrites, f.targetWrites, f.receiptWrites)
			}
		})
	}
}

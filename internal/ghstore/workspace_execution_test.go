package ghstore

import (
	"context"
	"strings"
	"testing"
)

func TestWorkspaceEntityMappingsUseNativeReceiptsAndFreshClock(t *testing.T) {
	for _, kind := range []string{"note", "board"} {
		t.Run(kind, func(t *testing.T) {
			f := newJournalWriteFixture(t, "")
			if err := f.client.TrackActions("ses_actor"); err != nil {
				t.Fatal(err)
			}
			op := "td-workspace-verify-" + kind
			id := ""
			if kind == "note" {
				note, err := f.client.CreateNoteWithOptions(context.Background(), "note", "body", "ses_actor", NoteCreateOptions{OperationID: op})
				if err != nil {
					t.Fatal(err)
				}
				id = note.ID
			} else {
				board, err := f.client.CreateBoardWithOptions(context.Background(), "board", "", "ses_actor", BoardCreateOptions{OperationID: op})
				if err != nil {
					t.Fatal(err)
				}
				id = board.ID
			}
			mapping, err := f.client.VerifiedEntityOperationMappings(context.Background(), map[string]string{op: kind})
			if err != nil || mapping[op] != id {
				t.Fatal(mapping, err)
			}
			if mapping, err := f.client.VerifiedEntityOperationMappings(context.Background(), map[string]string{op: kind, "td-missing": kind}); err == nil || mapping != nil {
				t.Fatal("partial mapping returned", mapping, err)
			}
			copy := *f.client
			copy.journal = &actionJournalScope{SessionID: "ses_other", Carrier: f.client.journal.Carrier}
			if _, err := copy.VerifiedEntityOperationMappings(context.Background(), map[string]string{op: kind}); err == nil {
				t.Fatal("foreign session claimed completion")
			}
			f.mode = "undo-actor"
			if _, err := f.client.VerifiedEntityOperationMappings(context.Background(), map[string]string{op: kind}); err == nil {
				t.Fatal("foreign GitHub author claimed completion")
			}
			f.mode = ""
			issue := f.issues[2]
			issue.UpdatedAt = issue.UpdatedAt.Add(1)
			f.issues[2] = issue
			if _, err := f.client.VerifiedEntityOperationMappings(context.Background(), map[string]string{op: kind}); err == nil {
				t.Fatal("changed server clock accepted")
			}
		})
	}
}

func TestWorkspaceEntityMappingsRejectIncompleteReceipt(t *testing.T) {
	f := newJournalWriteFixture(t, "receipt-denied")
	if err := f.client.TrackActions("ses_actor"); err != nil {
		t.Fatal(err)
	}
	op := "td-workspace-incomplete"
	if _, err := f.client.CreateNoteWithOptions(context.Background(), "note", "body", "ses_actor", NoteCreateOptions{OperationID: op}); err == nil {
		t.Fatal("fixture did not reject receipt")
	}
	f.mode = ""
	if result, err := f.client.VerifiedEntityOperationMappings(context.Background(), map[string]string{op: "note"}); err == nil || result != nil {
		t.Fatal("prepared receipt became completed", result, err)
	}
}

func TestWorkspaceArchiveDataRejectsInvalidInputBeforeBootstrap(t *testing.T) {
	f := newJournalWriteFixture(t, "")
	if err := f.client.TrackActions("ses_actor"); err != nil {
		t.Fatal(err)
	}
	f.client.run = func(context.Context, string, []byte, ...string) ([]byte, error) {
		t.Fatal("invalid archive reached API")
		return nil, nil
	}
	if _, err := f.client.PublishWorkspaceArchiveData(context.Background(), "td-workspace", []byte("invalid"), nil, 0); err == nil {
		t.Fatal("invalid archive accepted")
	}
	if _, err := f.client.VerifiedEntityOperationMappings(context.Background(), map[string]string{"td-op": "task"}); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatal(err)
	}
}

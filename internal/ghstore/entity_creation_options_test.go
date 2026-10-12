package ghstore

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/models"
)

func TestConfiguredNoteCreationFlagsAreInInitialJournaledPayload(t *testing.T) {
	f := newJournalWriteFixture(t, "")
	if err := f.client.TrackActions("ses_actor"); err != nil {
		t.Fatal(err)
	}
	deletedAt := time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC)
	n, err := f.client.CreateNoteWithOptions(context.Background(), "note", "content", "ses_actor", NoteCreateOptions{Pinned: true, Archived: true, Deleted: true, DeletedAt: &deletedAt})
	if err != nil {
		t.Fatal(err)
	}
	if !n.Pinned || !n.Archived || n.DeletedAt == nil || !n.DeletedAt.Equal(deletedAt) || f.creationWrites != 1 || f.targetWrites != 0 {
		t.Fatal("flags installed by later writes or lost")
	}
	action, err := decodeActionJournal(*f.comment, "gh-9")
	if err != nil || action == nil || action.Phase != "applied" || action.After.Body != n.observed.nativeBody || !strings.Contains(action.CreateInput.Body, `"pinned":true`) || !strings.Contains(action.CreateInput.Body, `"deleted_at"`) {
		t.Fatal("creation journal did not bind complete flags", err)
	}
	if len(n.observed.meta.Note.History) != 1 || n.observed.meta.Note.History[0].SessionID != "ses_actor" {
		t.Fatal("source actor/history activated")
	}
}

func TestConfiguredBoardCreationMembershipAndViewAreInInitialJournaledPayload(t *testing.T) {
	f := newJournalWriteFixture(t, "")
	if err := f.client.TrackActions("ses_actor"); err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC)
	b, err := f.client.CreateBoardWithOptions(context.Background(), "board", `id = "gh-1"`, "ses_actor", BoardCreateOptions{ViewMode: "backlog", Positions: []BoardPosition{{IssueID: "gh-1", Position: 7, AddedAt: stamp}}})
	if err != nil {
		t.Fatal(err)
	}
	if b.ViewMode != "backlog" || len(b.Details.Positions) != 1 || !b.Details.Positions[0].AddedAt.Equal(stamp) || f.creationWrites != 1 || f.targetWrites != 0 {
		t.Fatal("board configuration not installed atomically")
	}
	action, err := decodeActionJournal(*f.comment, "gh-9")
	if err != nil || action == nil || action.After.Body != b.observed.nativeBody || !strings.Contains(action.CreateInput.Body, `"view_mode":"backlog"`) || !strings.Contains(action.CreateInput.Body, `"position":7`) {
		t.Fatal("journal input differs from full board creation", err)
	}
}

func TestConfiguredBoardCreationRejectsInvalidOrForeignMembershipBeforeMutation(t *testing.T) {
	for _, stage := range []string{"mode", "duplicate_slot", "missing_task", "note_target"} {
		t.Run(stage, func(t *testing.T) {
			f := newJournalWriteFixture(t, "")
			options := BoardCreateOptions{ViewMode: "backlog", Positions: []BoardPosition{{IssueID: "gh-1", Position: 0, AddedAt: time.Now().UTC()}}}
			switch stage {
			case "mode":
				options.ViewMode = "unknown"
			case "duplicate_slot":
				options.Positions = append(options.Positions, BoardPosition{IssueID: "gh-2", Position: 0, AddedAt: time.Now().UTC()})
			case "missing_task":
				options.Positions[0].IssueID = "gh-404"
			case "note_target":
				body, err := encodeBody("note", metadata{EntityKind: "note", Type: models.TypeTask, Priority: models.PriorityP2, Note: &NoteDetails{Version: 1}})
				if err != nil {
					t.Fatal(err)
				}
				i := f.issues[1]
				i.Body = body
				f.issues[1] = i
			}
			if b, err := f.client.CreateBoardWithOptions(context.Background(), "board", "", "actor", options); err == nil || b != nil {
				t.Fatal("invalid position/options accepted")
			}
			if f.creationWrites+f.targetWrites+f.intentWrites+f.carrierWrites != 0 {
				t.Fatal("invalid input mutated GitHub")
			}
		})
	}
}

func TestConfiguredEntityCreationPinsOperationAndNeverReplays(t *testing.T) {
	for _, kind := range []string{"note", "board"} {
		for _, mode := range []string{"", "create-uncertain", "receipt-denied"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				f := newJournalWriteFixture(t, mode)
				if err := f.client.TrackActions("ses_actor"); err != nil {
					t.Fatal(err)
				}
				op := "td-workspace-fixed-" + kind
				name := kind
				create := func() error {
					if kind == "note" {
						_, err := f.client.CreateNoteWithOptions(context.Background(), name, "content", "ses_actor", NoteCreateOptions{OperationID: op})
						return err
					}
					_, err := f.client.CreateBoardWithOptions(context.Background(), name, "", "ses_actor", BoardCreateOptions{OperationID: op})
					return err
				}
				err := create()
				if (err != nil) != (mode != "") {
					t.Fatal(err)
				}
				if f.creationWrites != 1 {
					t.Fatal(f.creationWrites)
				}
				action, err := decodeActionJournal(*f.comment, "gh-9")
				if err != nil || action.OperationID != op {
					t.Fatal(action, err)
				}
				_, meta, _, err := decodeBody(f.issues[2].Body)
				if err != nil || meta.OperationID != op {
					t.Fatal(meta, err)
				}
				f.mode = ""
				// A different name must not bypass the operation replay guard.
				name = "different name"
				if err := create(); err == nil {
					t.Fatal("operation replay accepted")
				}
				if f.creationWrites != 1 || f.intentWrites != 1 {
					t.Fatal("same operation created another entity/intent", f)
				}
			})
		}
	}
}

func TestExplicitEntityCreationIdentityRejectsInvalidScopeBeforeAPI(t *testing.T) {
	for _, kind := range []string{"note", "board"} {
		for _, scope := range []string{"untracked", "wrong_actor", "invalid", "too_long"} {
			t.Run(kind+"/"+scope, func(t *testing.T) {
				f := newJournalWriteFixture(t, "")
				op, actor := "td-fixed", "ses_actor"
				if scope != "untracked" {
					if err := f.client.TrackActions(actor); err != nil {
						t.Fatal(err)
					}
				}
				switch scope {
				case "wrong_actor":
					actor = "ses_other"
				case "invalid":
					op = "../foreign"
				case "too_long":
					op = strings.Repeat("x", 129)
				}
				f.client.run = func(context.Context, string, []byte, ...string) ([]byte, error) {
					t.Fatal("invalid scope reached API")
					return nil, nil
				}
				var err error
				if kind == "note" {
					_, err = f.client.CreateNoteWithOptions(context.Background(), "note", "content", actor, NoteCreateOptions{OperationID: op})
				} else {
					_, err = f.client.CreateBoardWithOptions(context.Background(), "board", "", actor, BoardCreateOptions{OperationID: op})
				}
				if err == nil {
					t.Fatal("invalid scope accepted")
				}
			})
		}
	}
}

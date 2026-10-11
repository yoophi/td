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

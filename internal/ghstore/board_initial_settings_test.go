package ghstore

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/marcus/td/internal/models"
)

func TestBuiltinInitialConfigurationIsOneJournal(t *testing.T) {
	for _, operation := range []string{"view-mode", "position", "slot", "before"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			f := newJournalWriteFixture(t, "")
			if err := f.client.TrackActions("ses_actor"); err != nil {
				t.Fatal(err)
			}
			virtual, err := f.client.GetBoard(ctx, "bd-all-issues")
			if err != nil {
				t.Fatal(err)
			}
			// Public fields must not replace the canonical virtual defaults.
			virtual.Name, virtual.Details.Query = "Forged name", "status = closed"
			var created *BoardRecord
			switch operation {
			case "view-mode":
				created, err = f.client.SetBoardViewModeObserved(ctx, virtual, "backlog", "ses_actor")
			case "position":
				created, err = f.client.SetBoardPositionObserved(ctx, virtual, "gh-1", 123, "ses_actor")
			case "slot":
				created, err = f.client.MoveBoardPositionObserved(ctx, virtual, "gh-1", 7, "ses_actor")
			case "before":
				created, err = f.client.MoveBoardBeforeObserved(ctx, virtual, "gh-1", "", []models.Issue{{ID: "gh-1"}}, "ses_actor")
			}
			if err != nil {
				t.Fatal(err)
			}
			if f.creationWrites != 1 || f.targetWrites != 0 || f.intentWrites != 1 || f.receiptWrites != 1 {
				t.Fatalf("split initial configuration: create=%d patch=%d intent=%d receipt=%d", f.creationWrites, f.targetWrites, f.intentWrites, f.receiptWrites)
			}
			if created.ID != "bd-all-issues" || created.Number != 2 || created.Name != "All Issues" || created.Query != "" || len(created.Details.History) != 1 {
				t.Fatal(created)
			}
			if operation == "view-mode" {
				if created.ViewMode != "backlog" || len(created.Details.Positions) != 0 {
					t.Fatal(created.Details)
				}
			} else {
				key := 65536
				if operation == "position" {
					key = 123
				}
				if len(created.Details.Positions) != 1 || created.Details.Positions[0].IssueID != "gh-1" || created.Details.Positions[0].Position != key {
					t.Fatal(created.Details)
				}
			}
			history := created.Details.History[0]
			if history.Action != "create" || history.SessionID != "ses_actor" || !strings.HasPrefix(history.Reason, "Initial ") || (operation == "slot" && !strings.Contains(history.Reason, "slot 7")) {
				t.Fatal("original request was lost", history)
			}
			action, err := decodeActionJournal(*f.comment, "gh-9")
			if err != nil || action.Phase != "applied" || action.Action != "create" || action.SessionID != "ses_actor" || action.CreateInput.Body != action.After.Body || action.After.Body != created.observed.nativeBody {
				t.Fatal("incorrect creation evidence", action, err)
			}
			if _, err := f.client.UndoAction(ctx, action.ID, "ses_actor"); err == nil || !strings.Contains(err.Error(), "retain its carrier identity/history") || f.targetWrites != 0 {
				t.Fatal("builtin creation undo must explain retained identity", err)
			}
		})
	}
}

func TestBuiltinInitialConfigurationRejectsInvalidOrRacingObservation(t *testing.T) {
	ctx := context.Background()
	f := newJournalWriteFixture(t, "")
	if err := f.client.TrackActions("ses_actor"); err != nil {
		t.Fatal(err)
	}
	virtual, err := f.client.GetBoard(ctx, "bd-all-issues")
	if err != nil {
		t.Fatal(err)
	}
	name, mode := "Rename", "backlog"
	if _, err := f.client.UpdateBoardConfigObserved(ctx, virtual, BoardChanges{Name: &name}, &mode, "ses_actor"); err == nil {
		t.Fatal("builtin rename accepted")
	}
	if _, err := f.client.MoveBoardPositionObserved(ctx, virtual, "gh-1", 0, "ses_actor"); err == nil {
		t.Fatal("invalid slot accepted")
	}
	if _, err := f.client.MoveBoardBeforeObserved(ctx, virtual, "gh-1", "gh-3", []models.Issue{{ID: "gh-1"}}, "ses_actor"); err == nil {
		t.Fatal("missing anchor accepted")
	}
	other := *f.client
	other.repo = "other/repo"
	if _, err := other.SetBoardViewModeObserved(ctx, virtual, mode, "ses_actor"); err == nil {
		t.Fatal("foreign virtual observation accepted")
	}
	if f.creationWrites != 0 || f.intentWrites != 0 {
		t.Fatal("invalid request wrote")
	}
	base := f.client.run
	f.client.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		data, err := base(ctx, dir, payload, args...)
		if args[4] == "POST" && args[5] == "repos/owner/repo/issues/9/comments" {
			peer := f.issues[1]
			peer.Number, peer.Title = 2, "All Issues"
			peer.Body = boardMetadataFixture(t, "Peer carrier", BoardDetails{Version: 1, Builtin: true, ViewMode: "swimlanes"})
			f.issues[2] = peer
		}
		return data, err
	}
	_, err = f.client.SetBoardViewModeObserved(ctx, virtual, mode, "ses_actor")
	var conflict *ConflictError
	if !errors.As(err, &conflict) || conflict.AfterWrite || f.creationWrites != 0 || f.targetWrites != 0 || f.intentWrites != 1 {
		t.Fatal("carrier appearing during intent publication was overwritten or duplicated", err)
	}
}

func TestBuiltinInitialConfigurationFailureAndReadOnlyRecovery(t *testing.T) {
	for _, mode := range []string{"intent-denied", "create-denied", "create-uncertain", "create-readback", "receipt-denied", "receipt-uncertain"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			f := newJournalWriteFixture(t, mode)
			if err := f.client.TrackActions("ses_actor"); err != nil {
				t.Fatal(err)
			}
			virtual, err := f.client.GetBoard(ctx, "bd-all-issues")
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.client.SetBoardViewModeObserved(ctx, virtual, "backlog", "ses_actor")
			if err == nil || f.creationWrites > 1 || f.targetWrites != 0 {
				t.Fatal("uncertain result succeeded or was replayed", err)
			}
			if mode == "intent-denied" {
				if f.creationWrites != 0 {
					t.Fatal("intent failure created carrier")
				}
				return
			}
			if !strings.Contains(err.Error(), "journal ghc-101") {
				t.Fatal("error lacks durable recovery identity", err)
			}
			if mode == "create-denied" {
				if _, ok := f.issues[2]; ok {
					t.Fatal("denied creation wrote")
				}
				return
			}
			_, meta, _, err := decodeBody(f.issues[2].Body)
			if err != nil || meta.Board.ViewMode != "backlog" {
				t.Fatal("allocated carrier lacks initial settings", err)
			}
			f.mode = ""
			before := *f.comment
			receipts := f.receiptWrites
			preview, err := f.client.ReconcileAction(ctx, "ghc-101", "ses_recovery", false)
			if err != nil || preview.Applied || preview.TargetWriteReplayed || *f.comment != before || f.receiptWrites != receipts {
				t.Fatal("preview changed remote state", preview, err)
			}
			result, err := f.client.ReconcileAction(ctx, "ghc-101", "ses_recovery", true)
			if err != nil || !result.Applied || result.TargetWriteReplayed || result.Journal.SessionID != "ses_actor" || f.creationWrites != 1 || f.targetWrites != 0 {
				t.Fatal("reconciliation lost attribution or replayed target", result, err)
			}
			receipts = f.receiptWrites
			result, err = f.client.ReconcileAction(ctx, "ghc-101", "ses_recovery", true)
			if err != nil || !result.AlreadyReconciled || f.receiptWrites != receipts || f.creationWrites != 1 || f.targetWrites != 0 {
				t.Fatal("repeat reconciliation wrote", result, err)
			}
		})
	}
}

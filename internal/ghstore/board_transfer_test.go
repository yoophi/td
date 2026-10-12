package ghstore

import (
	"context"
	"strings"
	"testing"
	"time"
)

func transferBuiltinFixture(t *testing.T) (*journalWriteFixture, *BoardRecord) {
	t.Helper()
	f := newJournalWriteFixture(t, "")
	if err := f.client.TrackActions("ses_actor"); err != nil {
		t.Fatal(err)
	}
	board, err := f.client.GetBoard(context.Background(), "bd-all-issues")
	if err != nil {
		t.Fatal(err)
	}
	return f, board
}

func TestBuiltinTransferCreatesInitialSettingsInOneJournaledPayload(t *testing.T) {
	f, virtual := transferBuiltinFixture(t)
	stamp := time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC)
	b, err := f.client.ApplyBuiltinBoardTransferObserved(context.Background(), virtual, "ses_actor", BoardCreateOptions{OperationID: "td-workspace-builtin", ViewMode: "backlog", Positions: []BoardPosition{{IssueID: "gh-1", Position: 4, AddedAt: stamp}}})
	if err != nil || b.ID != "bd-all-issues" || !b.IsBuiltin || b.ViewMode != "backlog" || len(b.Details.Positions) != 1 || !b.Details.Positions[0].AddedAt.Equal(stamp) || f.creationWrites != 1 || f.targetWrites != 0 {
		t.Fatal(b, err, f)
	}
	action, err := decodeActionJournal(*f.comment, "gh-9")
	if err != nil || action.OperationID != "td-workspace-builtin" || action.Phase != "applied" || action.After.Body != b.observed.nativeBody {
		t.Fatal(action, err)
	}
	if _, err := f.client.ApplyBuiltinBoardTransferObserved(context.Background(), virtual, "ses_actor", BoardCreateOptions{OperationID: "td-workspace-builtin", ViewMode: "backlog"}); err == nil || f.creationWrites != 1 {
		t.Fatal("builtin creation replayed", err)
	}
}

func TestBuiltinTransferMergesPositionsKeepsDestinationSettingsAndPinsPatch(t *testing.T) {
	f, virtual := transferBuiltinFixture(t)
	stamp := time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC)
	before, err := f.client.ApplyBuiltinBoardTransferObserved(context.Background(), virtual, "ses_actor", BoardCreateOptions{OperationID: "td-builtin-initial", ViewMode: "swimlanes", Positions: []BoardPosition{{IssueID: "gh-1", Position: 0, AddedAt: stamp}}})
	if err != nil {
		t.Fatal(err)
	}
	newTask := f.issues[1]
	newTask.Number = 3
	f.issues[3] = newTask
	after, err := f.client.ApplyBuiltinBoardTransferObserved(context.Background(), before, "ses_actor", BoardCreateOptions{OperationID: "td-workspace-merge", ViewMode: "backlog", Positions: []BoardPosition{{IssueID: "gh-3", Position: 7, AddedAt: stamp}}})
	if err != nil || after.ViewMode != "swimlanes" || after.Name != before.Name || len(after.Details.Positions) != 2 || f.creationWrites != 1 || f.targetWrites != 1 {
		t.Fatal(after, err, f)
	}
	action, err := decodeActionJournal(*f.comment, "gh-9")
	if err != nil || action.OperationID != "td-workspace-merge" || action.Action != "board" || action.Phase != "applied" || action.Before.Body != before.observed.nativeBody || action.After.Body != after.observed.nativeBody {
		t.Fatal(action, err)
	}
	if _, err := f.client.ApplyBuiltinBoardTransferObserved(context.Background(), before, "ses_actor", BoardCreateOptions{OperationID: "td-workspace-merge", Positions: []BoardPosition{{IssueID: "gh-3", Position: 7, AddedAt: stamp}}}); err == nil || f.targetWrites != 1 {
		t.Fatal("original observed patch replayed", err)
	}
	// Re-observing must not permit reuse of the same operation on a new slot.
	newTask.Number = 4
	f.issues[4] = newTask
	_, err = f.client.ApplyBuiltinBoardTransferObserved(context.Background(), after, "ses_actor", BoardCreateOptions{OperationID: "td-workspace-merge", Positions: []BoardPosition{{IssueID: "gh-4", Position: 8, AddedAt: stamp}}})
	if err == nil || !strings.Contains(err.Error(), "no target PATCH attempted") || f.targetWrites != 1 {
		t.Fatal("operation guard bypassed by fresh observation", err, f.targetWrites)
	}
}

func TestBuiltinTransferRejectsConflictingPositionsBeforeAPI(t *testing.T) {
	f, virtual := transferBuiltinFixture(t)
	stamp := time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC)
	before, err := f.client.ApplyBuiltinBoardTransferObserved(context.Background(), virtual, "ses_actor", BoardCreateOptions{ViewMode: "swimlanes", Positions: []BoardPosition{{IssueID: "gh-1", Position: 0, AddedAt: stamp}}})
	if err != nil {
		t.Fatal(err)
	}
	f.client.run = func(context.Context, string, []byte, ...string) ([]byte, error) {
		t.Fatal("conflict reached API")
		return nil, nil
	}
	for _, position := range []BoardPosition{{IssueID: "gh-3", Position: 0, AddedAt: stamp}, {IssueID: "gh-1", Position: 7, AddedAt: stamp}} {
		if _, err := f.client.ApplyBuiltinBoardTransferObserved(context.Background(), before, "ses_actor", BoardCreateOptions{OperationID: "td-conflict", Positions: []BoardPosition{position}}); err == nil {
			t.Fatal("conflicting slot/member accepted")
		}
	}
}

func TestBuiltinTransferPartialFailureRetainsOriginalOperationWithoutRetry(t *testing.T) {
	for _, mode := range []string{"target-denied", "target-uncertain", "receipt-denied"} {
		t.Run(mode, func(t *testing.T) {
			f, virtual := transferBuiltinFixture(t)
			before, err := f.client.ApplyBuiltinBoardTransferObserved(context.Background(), virtual, "ses_actor", BoardCreateOptions{OperationID: "td-initial", ViewMode: "swimlanes"})
			if err != nil {
				t.Fatal(err)
			}
			newTask := f.issues[1]
			newTask.Number = 3
			f.issues[3] = newTask
			f.targetNumber = 2
			f.mode = mode
			_, err = f.client.ApplyBuiltinBoardTransferObserved(context.Background(), before, "ses_actor", BoardCreateOptions{OperationID: "td-workspace-partial", Positions: []BoardPosition{{IssueID: "gh-3", Position: 7, AddedAt: time.Now().UTC()}}})
			if err == nil || f.targetWrites != 1 {
				t.Fatal("failure hidden or retried", err, f.targetWrites)
			}
			action, decodeErr := decodeActionJournal(*f.comment, "gh-9")
			if decodeErr != nil || action.OperationID != "td-workspace-partial" || action.Phase != "prepared" || action.UpdateIntent == nil {
				t.Fatal(action, decodeErr)
			}
			if mode == "target-denied" && !strings.Contains(err.Error(), "HTTP 403 target permission denied") {
				t.Fatal("original error lost", err)
			}
			if mode == "target-uncertain" && !strings.Contains(err.Error(), "connection lost") {
				t.Fatal("uncertain error lost", err)
			}
			f.mode = ""
			if _, err := f.client.ApplyBuiltinBoardTransferObserved(context.Background(), before, "ses_actor", BoardCreateOptions{OperationID: "td-workspace-partial", Positions: []BoardPosition{{IssueID: "gh-3", Position: 7, AddedAt: time.Now().UTC()}}}); err == nil || f.targetWrites != 1 {
				t.Fatal("partial operation replayed", err, f.targetWrites)
			}
		})
	}
}

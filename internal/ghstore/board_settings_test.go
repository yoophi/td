package ghstore

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestBoardSettingsAndMovesPersistTogether(t *testing.T) {
	ctx := context.Background()
	c, issues, writes := hierarchyFixture(t)
	b, err := c.CreateBoard(ctx, "Sprint", "status = open", "creator")
	if err != nil {
		t.Fatal(err)
	}
	b, err = c.SetBoardViewModeObserved(ctx, b, "backlog", "web")
	if err != nil {
		t.Fatal(err)
	}
	b, err = c.MarkBoardViewedObserved(ctx, b, "web")
	if err != nil {
		t.Fatal(err)
	}
	b, err = c.SetBoardPositionObserved(ctx, b, "gh-1", 0, "web")
	if err != nil {
		t.Fatal(err)
	}
	b, err = c.SetBoardPositionObserved(ctx, b, "gh-2", 1, "web")
	if err != nil {
		t.Fatal(err)
	}
	before := *writes
	if _, err = c.SetBoardPositionObserved(ctx, b, "gh-3", 1, "web"); err == nil || *writes != before {
		t.Fatalf("duplicate slot accepted: %v", err)
	}
	b, err = c.MoveBoardPositionObserved(ctx, b, "gh-2", 1, "web")
	if err != nil {
		t.Fatal(err)
	}
	if b.Details.Positions[0].IssueID != "gh-2" || b.Details.Positions[1].IssueID != "gh-1" || b.Details.Positions[0].Position != 65536 || b.Details.Positions[1].Position != 131072 || *writes != before+1 {
		t.Fatalf("move %+v writes=%d", b.Details.Positions, *writes)
	}
	if b.Details.ViewMode != "backlog" || b.Details.LastViewedAt == nil || b.Details.Query != "status = open" {
		t.Fatalf("settings lost %+v", b.Details)
	}
	// Cleanup must work even when the target has disappeared.
	delete(issues, 1)
	b, err = c.RemoveBoardPositionObserved(ctx, b, "gh-1", "web")
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Details.Positions) != 1 || b.Details.History[len(b.Details.History)-1].Action != "remove_position" {
		t.Fatalf("cleanup %+v", b.Details)
	}
	before = *writes
	if _, err = c.MoveBoardPositionObserved(ctx, b, "gh-1", 2, "web"); err == nil || *writes != before {
		t.Fatalf("missing target accepted: %v", err)
	}
}

func TestBoardSettingsPreservePrivateObservationAndRejectStaleWrites(t *testing.T) {
	ctx := context.Background()
	c, issues, writes := hierarchyFixture(t)
	b, err := c.CreateBoard(ctx, "Sprint", "", "actor")
	if err != nil {
		t.Fatal(err)
	}
	b.Details.Query = "status = closed"
	updated, err := c.SetBoardViewModeObserved(ctx, b, "backlog", "actor")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Details.Query != "" {
		t.Fatal("public mutation leaked")
	}
	before := *writes
	var conflict *ConflictError
	if _, err = c.MarkBoardViewedObserved(ctx, b, "actor"); !errors.As(err, &conflict) || conflict.AfterWrite || *writes != before {
		t.Fatalf("stale write %v", err)
	}
	for _, mode := range []string{"", "unknown"} {
		if _, err = c.SetBoardViewModeObserved(ctx, updated, mode, "actor"); err == nil {
			t.Fatal("invalid mode")
		}
	}
	for _, id := range []string{"1", "#1", "gh-01", "gh-4"} {
		if _, err = c.SetBoardPositionObserved(ctx, updated, id, 0, "actor"); err == nil {
			t.Fatalf("invalid/aux target %s", id)
		}
	}
	if _, err = c.SetBoardPositionObserved(ctx, updated, "gh-1", -1, "actor"); err == nil {
		t.Fatal("negative key")
	}
	if _, err = c.MoveBoardPositionObserved(ctx, updated, "gh-1", 0, "actor"); err == nil {
		t.Fatal("zero slot")
	}
	if _, err = c.RemoveBoardPositionObserved(ctx, updated, "gh-1", "actor"); err == nil {
		t.Fatal("missing position")
	}
	if *writes != before || issues[4]["title"] != "Sprint" {
		t.Fatal("invalid settings wrote")
	}
}

func TestBoardBuiltinExplicitPersistenceAndSettings(t *testing.T) {
	ctx := context.Background()
	c, _, writes := hierarchyFixture(t)
	if _, err := c.GetBoard(ctx, "bd-all-issues"); err != nil || *writes != 0 {
		t.Fatalf("read created builtin %v", err)
	}
	b, err := c.EnsureBuiltinBoard(ctx, "actual-web")
	if err != nil {
		t.Fatal(err)
	}
	if b.Number != 4 || b.ID != "bd-all-issues" || !b.IsBuiltin || *writes != 1 {
		t.Fatalf("builtin %+v", b)
	}
	b, err = c.SetBoardViewModeObserved(ctx, b, "backlog", "actual-web")
	if err != nil {
		t.Fatal(err)
	}
	b, err = c.SetBoardPositionObserved(ctx, b, "gh-1", 0, "actual-web")
	if err != nil {
		t.Fatal(err)
	}
	b, err = c.MarkBoardViewedObserved(ctx, b, "actual-web")
	if err != nil {
		t.Fatal(err)
	}
	if b.Details.ViewMode != "backlog" || b.Details.LastViewedAt == nil || len(b.Details.Positions) != 1 {
		t.Fatalf("builtin settings %+v", b)
	}
	before := *writes
	again, err := c.EnsureBuiltinBoard(ctx, "actual-web")
	if err != nil || again.Number != 4 || *writes != before {
		t.Fatalf("duplicate builtin %+v %v", again, err)
	}
	name := "Renamed"
	if _, err = c.UpdateBoardObserved(ctx, b, BoardChanges{Name: &name}, "actual-web"); err == nil {
		t.Fatal("builtin renamed")
	}
	if _, err = c.DeleteBoardObserved(ctx, b, "actual-web", ""); err == nil {
		t.Fatal("builtin deleted")
	}
	if *writes != before {
		t.Fatal("builtin policy wrote")
	}
}

func TestBoardBuiltinRaceDoesNotChooseDuplicate(t *testing.T) {
	ctx := context.Background()
	c, issues, writes := hierarchyFixture(t)
	base := c.run
	c.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		data, err := base(ctx, dir, payload, args...)
		if slices.Contains(args, "POST") {
			issues[5] = map[string]any{"number": 5, "state": "open", "title": "All Issues", "body": boardMetadataFixture(t, "peer builtin", BoardDetails{Version: 1, Builtin: true, ViewMode: "swimlanes"})}
		}
		return data, err
	}
	_, err := c.EnsureBuiltinBoard(ctx, "actor")
	if err == nil || !strings.Contains(err.Error(), "was created") || !strings.Contains(err.Error(), "multiple All Issues") || *writes != 1 {
		t.Fatalf("%v writes=%d", err, *writes)
	}
	if _, err = c.GetBoard(ctx, "bd-all-issues"); err == nil {
		t.Fatal("duplicate silently selected")
	}
}

func TestBoardMoveLargeSlotAndCleanupPreserveOtherPositionClocks(t *testing.T) {
	ctx := context.Background()
	c, _, writes := hierarchyFixture(t)
	b, err := c.CreateBoard(ctx, "Sprint", "", "actor")
	if err != nil {
		t.Fatal(err)
	}
	b, err = c.SetBoardPositionObserved(ctx, b, "gh-1", 0, "actor")
	if err != nil {
		t.Fatal(err)
	}
	stamp := b.Details.Positions[0].AddedAt
	b, err = c.MoveBoardPositionObserved(ctx, b, "gh-2", int(^uint(0)>>1), "actor")
	if err != nil {
		t.Fatal(err)
	}
	if b.Details.Positions[0].IssueID != "gh-1" || b.Details.Positions[1].IssueID != "gh-2" || !b.Details.Positions[0].AddedAt.Equal(stamp) {
		t.Fatalf("move %+v", b.Details.Positions)
	}
	before := *writes
	b, err = c.RemoveBoardPositionObserved(ctx, b, "gh-2", "actor")
	if err != nil {
		t.Fatal(err)
	}
	if *writes != before+1 || len(b.Details.Positions) != 1 || !b.Details.Positions[0].AddedAt.Equal(stamp) {
		t.Fatalf("remove %+v", b.Details.Positions)
	}
}

func TestBoardObservedBuiltinMaterializationRejectsAppearedCarrier(t *testing.T) {
	ctx := context.Background()
	c, _, writes := hierarchyFixture(t)
	virtual, err := c.GetBoard(ctx, "bd-all-issues")
	if err != nil {
		t.Fatal(err)
	}
	other := *c
	other.repo = "other/repo"
	if _, err := other.MaterializeBuiltinBoardObserved(ctx, virtual, "actor"); err == nil {
		t.Fatal("foreign virtual observation accepted")
	}
	if _, err := other.BoardQueryObserved(virtual); err == nil {
		t.Fatal("foreign virtual query accepted")
	}
	b, err := c.MaterializeBuiltinBoardObserved(ctx, virtual, "actor")
	if err != nil {
		t.Fatal(err)
	}
	if b.Number == 0 || *writes != 1 {
		t.Fatalf("%+v writes=%d", b, *writes)
	}
	var conflict *ConflictError
	if _, err = c.MaterializeBuiltinBoardObserved(ctx, virtual, "actor"); !errors.As(err, &conflict) || conflict.AfterWrite || *writes != 1 {
		t.Fatalf("appeared carrier %v", err)
	}
	if _, err = c.MaterializeBuiltinBoardObserved(ctx, b, "actor"); err == nil {
		t.Fatal("persisted observation materialized")
	}
	if _, err = c.MaterializeBuiltinBoardObserved(ctx, virtual, " "); err == nil {
		t.Fatal("missing actor")
	}
}

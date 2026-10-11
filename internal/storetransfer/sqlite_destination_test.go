package storetransfer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/db"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
)

func TestExecuteSQLiteBuiltinConfigurationReuseIsDisclosed(t *testing.T) {
	ctx := context.Background()
	s := githubTaskInputFixture(t)
	now := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	s.GitHub.Boards = []ghstore.BoardRecord{{Board: models.Board{ID: "bd-all-issues", Name: "source builtin", IsBuiltin: true, ViewMode: "backlog", CreatedAt: now, UpdatedAt: now}, Number: 3, Details: ghstore.BoardDetails{Version: 1, Builtin: true, ViewMode: "backlog", Positions: []ghstore.BoardPosition{{IssueID: "gh-1", Position: 0, AddedAt: now}}}}}
	s.GitHub.Native = append(s.GitHub.Native, ghstore.NativeExport{Number: 3, Kind: "board", Title: "source builtin"})
	source, err := NewGitHub(s.GitHub, 0)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	target, err := InspectTarget(ctx, TargetSpec{ProjectDir: root, Store: "sqlite", Namespace: "target"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildDataPlan(source, target.Observation, nil)
	if err != nil {
		t.Fatal(err)
	}
	disclosed := false
	for _, d := range plan.Disclosures {
		if d.Code == "builtin_sqlite_board_configuration" && d.Handling == "reuse_existing" && d.Count == 1 {
			disclosed = true
		}
		if d.Code == "boards_configuration_queries" && d.Count != 0 {
			t.Fatal("builtin configuration falsely counted as copied")
		}
	}
	if !disclosed {
		t.Fatal("builtin reuse not disclosed")
	}
	result, err := ExecuteSQLiteDestination(ctx, source, target, nil, 0)
	if err != nil || result.Boards != 0 || result.Positions != 1 {
		t.Fatal("builtin execution/counts differ", err)
	}
	actual, err := db.ReadSQLiteTransferDestination(ctx, root, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(actual.Boards) != 1 || actual.Boards[0].Board.Name != "All Issues" || actual.Boards[0].Board.ViewMode != "swimlanes" || len(actual.Boards[0].Positions) != 1 {
		t.Fatal("builtin configuration overwritten or position lost")
	}
}

func TestPrepareNewSQLiteDestinationAndApply(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	target, err := InspectTarget(ctx, TargetSpec{ProjectDir: root, Store: "sqlite", Namespace: "target"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareSQLiteDestination(ctx, githubTaskInputFixture(t), target, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := db.OpenSQLiteTransferDestination(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Close() }()
	if err := prepared.Apply(ctx, writer); err != nil {
		t.Fatal(err)
	}
	actual, err := db.ReadSQLiteTransferDestination(ctx, root, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(actual.Tasks) != 2 || len(actual.Boards) != 1 || len(actual.TransferArchives) != 1 {
		t.Fatal("new target incomplete")
	}
}

func TestExecuteSQLiteDestinationRejectsFreshSourceReplayByQualifiedIdentity(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	source := githubTaskInputFixture(t)
	target, err := InspectTarget(ctx, TargetSpec{ProjectDir: root, Store: "sqlite", Namespace: "target"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	first, err := ExecuteSQLiteDestination(ctx, source, target, nil, 0)
	if err != nil || !first.Committed {
		t.Fatal("first execution failed", err)
	}
	source.GitHub.ObservedAt = time.Now().UTC()
	fresh, err := NewGitHub(source.GitHub, 0)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Envelope.SHA256 == first.SourceSHA256 {
		t.Fatal("fixture did not change observation checksum")
	}
	target, err = InspectTarget(ctx, TargetSpec{ProjectDir: root, Store: "sqlite", Namespace: "target"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := ExecuteSQLiteDestination(ctx, fresh, target, nil, 0); err == nil || result != nil || !strings.Contains(err.Error(), "previously transferred") {
		t.Fatal("fresh source duplicated identities", err)
	}
	actual, err := db.ReadSQLiteTransferDestination(ctx, root, 0)
	if err != nil || len(actual.Tasks) != 2 || len(actual.TransferArchives) != 1 {
		t.Fatal("rejected replay changed destination", err)
	}
}

func TestPrepareNewSQLiteDestinationChecksBeforeInitializationAndRejectsLaterChanges(t *testing.T) {
	for _, stage := range []string{"capacity", "unknown_mapping", "appeared", "after_init"} {
		t.Run(stage, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			target, err := InspectTarget(ctx, TargetSpec{ProjectDir: root, Store: "sqlite", Namespace: "target"}, 0)
			if err != nil {
				t.Fatal(err)
			}
			limit := int64(0)
			var bindings map[string]string
			if stage == "capacity" {
				limit = 1
			}
			if stage == "unknown_mapping" {
				bindings = map[string]string{"unknown": "td-target"}
			}
			if stage == "appeared" {
				if _, err := db.InitializeSQLiteTransferDestination(ctx, root); err != nil {
					t.Fatal(err)
				}
			}
			prepared, err := PrepareSQLiteDestination(ctx, githubTaskInputFixture(t), target, bindings, limit)
			if stage != "after_init" {
				if err == nil || prepared != nil {
					t.Fatal("invalid new target accepted")
				}
				if stage != "appeared" {
					if _, err := os.Stat(filepath.Join(root, ".todos")); !os.IsNotExist(err) {
						t.Fatal("failed preflight initialized target", err)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			writer, err := db.OpenSQLiteTransferDestination(ctx, root)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = writer.Close() }()
			if _, err := writer.CreateNote("external", "change"); err != nil {
				t.Fatal(err)
			}
			if err := prepared.Apply(ctx, writer); err == nil {
				t.Fatal("post-publication edits accepted as baseline")
			}
			actual, err := db.ReadSQLiteTransferDestination(ctx, root, 0)
			if err != nil {
				t.Fatal(err)
			}
			if len(actual.Tasks) != 0 || len(actual.Notes) != 1 || len(actual.TransferArchives) != 0 {
				t.Fatal("external edits lost or transfer partially committed")
			}
		})
	}
}

package db

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/transferlimit"
)

func TestOpenSQLiteTransferDestinationIsExplicitExistingAndNeverMigrates(t *testing.T) {
	ctx := context.Background()
	missing := t.TempDir()
	if err := os.Mkdir(filepath.Join(missing, ".todos"), 0755); err != nil {
		t.Fatal(err)
	}
	if database, err := OpenSQLiteTransferDestination(ctx, missing); err == nil || database != nil {
		t.Fatal("missing database created")
	}
	if _, err := os.Stat(filepath.Join(missing, dbFile)); !os.IsNotExist(err) {
		t.Fatal("missing destination mutated", err)
	}
	root := filepath.Join(t.TempDir(), "original")
	database, err := Initialize(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	literal := root + "?cache=shared#literal"
	if err := os.Rename(root, literal); err != nil {
		t.Fatal(err)
	}
	if err := config.SetStore(literal, "gh-issue", &models.GitHubStoreConfig{Remote: "origin", Repo: "owner/repo"}); err != nil {
		t.Fatal(err)
	}
	writer, err := OpenSQLiteTransferDestination(ctx, literal)
	if err != nil {
		t.Fatal("explicit target with literal path and GH selection rejected", err)
	}
	if _, err := writer.conn.Exec("UPDATE schema_info SET value='37' WHERE key='version'"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(literal, dbFile))
	if err != nil {
		t.Fatal(err)
	}
	if writer, err := OpenSQLiteTransferDestination(ctx, literal); err == nil || writer != nil || !strings.Contains(err.Error(), "no migration attempted") {
		t.Fatal("older schema opened/migrated", err)
	}
	after, err := os.ReadFile(filepath.Join(literal, dbFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("older schema database changed")
	}
}

func transferFixture(t *testing.T) (*DB, *SQLiteWorkspaceExport, *SQLiteWorkspaceExport, WorkspaceTransferArchive) {
	t.Helper()
	database, err := Initialize(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	database.SetMaxOpenConns(1)
	expected, err := database.readWorkspace(context.Background(), SchemaVersion)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	incoming := &SQLiteWorkspaceExport{SchemaVersion: SchemaVersion, Tasks: []SQLiteExportTask{
		{Issue: models.Issue{ID: "td-child", Title: "child", Type: models.TypeTask, Priority: models.PriorityP1, Status: models.StatusInProgress, ParentID: "td-parent", CreatedAt: now, UpdatedAt: now, DeletedAt: &now}, Logs: []models.Log{{ID: "import-log", IssueID: "td-child", SessionID: "imported-source:original", Message: "history", Type: models.LogTypeProgress, Timestamp: now}}, Dependencies: []models.IssueDependency{{IssueID: "td-child", DependsOnID: "td-parent", RelationType: "depends_on"}}},
		{Issue: models.Issue{ID: "td-parent", Title: "parent", Type: models.TypeTask, Priority: models.PriorityP2, Status: models.StatusOpen, CreatedAt: now, UpdatedAt: now}},
	}, Notes: []models.Note{{ID: "nt-new", Title: "note", Content: "content", CreatedAt: now, UpdatedAt: now, Pinned: true, Archived: true, DeletedAt: &now}}, Boards: []SQLiteExportBoard{{Board: models.Board{ID: "bd-new", Name: "board", Query: `id = "td-child"`, ViewMode: "backlog", CreatedAt: now, UpdatedAt: now}, Positions: []SQLiteExportPosition{{BoardIssue: models.BoardIssue{BoardID: "bd-new", IssueID: "td-child", Position: 0, AddedAt: now}}}}}}
	sha := strings.Repeat("a", 64)
	archive := WorkspaceTransferArchive{SourceSHA256: sha, TargetNamespace: "target", SourceEnvelope: json.RawMessage(`{"sha256":"` + sha + `","fixture":"opaque passive original input"}`), Mappings: map[string]string{"source:task:child": "td-child", "source:task:parent": "td-parent", "source:note": "nt-new", "source:board": "bd-new"}}
	return database, expected, incoming, archive
}

func TestWorkspaceTransferDuplicateArchiveAndRecollectionCapacity(t *testing.T) {
	database, expected, incoming, archive := transferFixture(t)
	if err := database.ApplyWorkspaceTransfer(context.Background(), expected, incoming, archive, 0); err != nil {
		t.Fatal(err)
	}
	before, err := database.readWorkspace(context.Background(), SchemaVersion)
	if err != nil {
		t.Fatal(err)
	}
	// A new observation does not make the same archive a new operation.
	if err := database.ApplyWorkspaceTransfer(context.Background(), before, incoming, archive, 0); err == nil || !strings.Contains(err.Error(), "inspect existing receipts") {
		t.Fatal("duplicate source archive accepted", err)
	}
	after, err := database.readWorkspace(context.Background(), SchemaVersion)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(before)
	b, _ := json.Marshal(after)
	if string(a) != string(b) {
		t.Fatal("duplicate operation changed destination")
	}
	budget, err := transferlimit.New(1)
	if err != nil {
		t.Fatal(err)
	}
	if rows, err := database.readTransferArchives(context.Background(), budget); rows != nil || !errors.Is(err, transferlimit.ErrCapacity) {
		t.Fatal("archive capacity error returned partial records", err)
	}
}

func TestWorkspaceTransferCommitsAllEntitiesAndPassiveArchive(t *testing.T) {
	database, expected, incoming, archive := transferFixture(t)
	if err := database.ApplyWorkspaceTransfer(context.Background(), expected, incoming, archive, 0); err != nil {
		t.Fatal(err)
	}
	actual, err := database.readWorkspace(context.Background(), SchemaVersion)
	if err != nil {
		t.Fatal(err)
	}
	if len(actual.Tasks) != 2 || len(actual.Notes) != 1 || len(actual.TransferArchives) != 1 || actual.TransferArchives[0].Mappings["source:task:child"] != "td-child" {
		t.Fatal("entities or capsule missing")
	}
	if string(actual.TransferArchives[0].SourceEnvelope) != string(archive.SourceEnvelope) {
		t.Fatal("passive original input changed")
	}
	if actual.Notes[0].DeletedAt == nil || !actual.Notes[0].Archived || !actual.Notes[0].Pinned {
		t.Fatal("note flags lost")
	}
	var positions int
	if err := database.conn.QueryRow("SELECT count(*) FROM board_issue_positions WHERE board_id='bd-new'").Scan(&positions); err != nil || positions != 1 {
		t.Fatal("board positions lost", err)
	}
	var active int
	if err := database.conn.QueryRow("SELECT count(*) FROM issue_reviews").Scan(&active); err != nil || active != 0 {
		t.Fatal("source reviews activated", err)
	}
	rows, err := database.conn.Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		t.Fatal("late sibling references became orphans")
	}
}

func TestWorkspaceTransferFailureRollsBackRowsAndArchiveDDL(t *testing.T) {
	for _, stage := range []string{"note_collision", "log_collision", "board_id_collision", "position_collision", "missing_dependency", "missing_parent", "position_owner", "active_actor", "capacity", "cancel", "stale"} {
		t.Run(stage, func(t *testing.T) {
			database, expected, incoming, archive := transferFixture(t)
			ctx := context.Background()
			limit := int64(0)
			switch stage {
			case "note_collision":
				incoming.Notes = append(incoming.Notes, incoming.Notes[0])
			case "log_collision":
				incoming.Tasks[0].Logs = append(incoming.Tasks[0].Logs, incoming.Tasks[0].Logs[0])
			case "board_id_collision":
				other := incoming.Boards[0]
				other.Positions = nil
				incoming.Boards = append(incoming.Boards, other)
			case "position_collision":
				other := incoming.Boards[0].Positions[0]
				other.IssueID = "td-parent"
				incoming.Boards[0].Positions = append(incoming.Boards[0].Positions, other)
			case "missing_dependency":
				incoming.Tasks[0].Dependencies[0].DependsOnID = "td-missing"
			case "missing_parent":
				incoming.Tasks[0].Issue.ParentID = "td-missing"
			case "position_owner":
				incoming.Boards[0].Positions[0].BoardID = "bd-other"
			case "active_actor":
				incoming.Tasks[0].Issue.ImplementerSession = "original"
			case "capacity":
				limit = 1
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "stale":
				if _, err := database.CreateNote("external", "change"); err != nil {
					t.Fatal(err)
				}
			}
			if err := database.ApplyWorkspaceTransfer(ctx, expected, incoming, archive, limit); err == nil {
				t.Fatal("invalid transfer succeeded")
			}
			var tasks, archives int
			if err := database.conn.QueryRow("SELECT count(*) FROM issues").Scan(&tasks); err != nil || tasks != 0 {
				t.Fatal("partial task writes survived", tasks, err)
			}
			if err := database.conn.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='store_transfer_archives'").Scan(&archives); err != nil || archives != 0 {
				t.Fatal("partial archive/schema survived", archives, err)
			}
			var notes int
			if err := database.conn.QueryRow("SELECT count(*) FROM notes").Scan(&notes); err != nil {
				t.Fatal(err)
			}
			want := 0
			if stage == "stale" {
				want = 1
			}
			if notes != want {
				t.Fatal("partial note writes survived or external data changed")
			}
		})
	}
}

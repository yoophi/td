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

func TestPreparedSQLiteWriteDoesNotInitializeUnobservedDestination(t *testing.T) {
	root := t.TempDir()
	target, err := InspectTarget(context.Background(), TargetSpec{ProjectDir: root, Store: "sqlite", Namespace: "target"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if prepared, err := PrepareSQLiteWrite(githubTaskInputFixture(t), target, nil, 0); err == nil || prepared != nil {
		t.Fatal("uninitialized target accepted")
	}
	if _, err := os.Stat(filepath.Join(root, ".todos")); !os.IsNotExist(err) {
		t.Fatal("preparation initialized target", err)
	}
}

func TestPreparedSQLiteWriteAdoptedNotesAndBoardsRemainUnchanged(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	database, err := db.Initialize(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	note, err := database.CreateNote("destination note", "destination body")
	if err != nil {
		t.Fatal(err)
	}
	board, err := database.CreateBoard("destination board", "")
	if err != nil {
		t.Fatal(err)
	}
	s := githubTaskInputFixture(t)
	now := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	s.GitHub.Notes = []ghstore.NoteRecord{{Note: models.Note{ID: "nt-gh-5", Title: "source note", Content: "source body", Pinned: true, CreatedAt: now, UpdatedAt: now}, Number: 5}}
	s.GitHub.Boards = []ghstore.BoardRecord{{Board: models.Board{ID: "bd-gh-3", Name: "source board", Query: `id = "gh-999"`, ViewMode: "backlog", CreatedAt: now, UpdatedAt: now}, Number: 3, Details: ghstore.BoardDetails{Version: 1, Query: `id = "gh-999"`, ViewMode: "backlog", Positions: []ghstore.BoardPosition{{IssueID: "gh-1", Position: 0, AddedAt: now}}}}}
	s.GitHub.Native = append(s.GitHub.Native, ghstore.NativeExport{Number: 5, Kind: "note", Title: "source note"}, ghstore.NativeExport{Number: 3, Kind: "board", Title: "source board"})
	source, err := NewGitHub(s.GitHub, 0)
	if err != nil {
		t.Fatal(err)
	}
	target, err := InspectTarget(ctx, TargetSpec{ProjectDir: root, Store: "sqlite", Namespace: "target"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := source.Preview()
	if err != nil {
		t.Fatal(err)
	}
	bindings := map[string]string{}
	for _, i := range preview.Identities {
		if i.Kind == "note" {
			bindings[i.Key] = note.ID
		}
		if i.Kind == "board" {
			bindings[i.Key] = board.ID
		}
	}
	plan, err := BuildDataPlan(source, target.Observation, bindings)
	if err != nil {
		t.Fatal(err)
	}
	if plan.BoardQueries[0].Handling != "passive_archive" || plan.Positions[0].Handling != "passive_archive" {
		t.Fatal("adopted board still pending live writes")
	}
	prepared, err := PrepareSQLiteWrite(source, target, bindings, 0)
	if err != nil {
		t.Fatal("unused adopted query blocked write", err)
	}
	if err := prepared.Apply(ctx, database); err != nil {
		t.Fatal(err)
	}
	actual, err := db.ReadSQLiteTransferDestination(ctx, root, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(actual.Notes) != 1 || actual.Notes[0].Title != "destination note" || actual.Notes[0].Content != "destination body" || actual.Notes[0].Pinned {
		t.Fatal("adopted note overwritten")
	}
	for _, b := range actual.Boards {
		if b.Board.ID == board.ID && (b.Board.Name != "destination board" || b.Board.Query != "" || len(b.Positions) != 0) {
			t.Fatal("adopted board overwritten")
		}
	}
	if len(actual.Tasks) != 2 || len(actual.TransferArchives) != 1 {
		t.Fatal("new tasks or passive adopted source missing")
	}
}

func TestPreparedSQLiteWriteAdoptsExplicitTaskWithoutOverwriting(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	database, err := db.Initialize(root)
	if err != nil {
		t.Fatal(err)
	}
	existing := models.Issue{Title: "destination content", Type: models.TypeTask, Priority: models.PriorityP1, Status: models.StatusOpen, CreatorSession: "destination-creator"}
	if err := database.CreateIssue(&existing); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	target, err := InspectTarget(ctx, TargetSpec{ProjectDir: root, Store: "sqlite", Namespace: "target"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	source := githubTaskInputFixture(t)
	preview, err := source.Preview()
	if err != nil {
		t.Fatal(err)
	}
	bindings := map[string]string{}
	var childKey string
	for _, i := range preview.Identities {
		if i.ID == "gh-2" {
			bindings[i.Key] = existing.ID
		} else {
			childKey = i.Key
		}
	}
	prepared, err := PrepareSQLiteWrite(source, target, bindings, 0)
	if err != nil {
		t.Fatal(err)
	}
	childID := prepared.Mappings()[childKey]
	prepared.Mappings()[childKey] = "td-tampered-copy"
	// Mutating the exposed typed projection after preparation cannot mutate
	// frozen destination expectations or the archived source bytes.
	target.Observation.SQLite.Tasks[0].Issue.Title = "tampered typed observation"
	source.GitHub.Tasks[0].Record.Title = "tampered typed input"
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
	if len(actual.Tasks) != 2 || len(actual.TransferArchives) != 1 {
		t.Fatal("missing tasks/archive")
	}
	for _, row := range actual.Tasks {
		if row.Issue.ID == existing.ID {
			if row.Issue.Title != "destination content" || row.Issue.CreatorSession != "destination-creator" || len(row.Logs) != 0 {
				t.Fatal("adopted task overwritten")
			}
		} else if row.Issue.ID != childID || row.Issue.ParentID != existing.ID || len(row.Dependencies) != 1 || row.Dependencies[0].DependsOnID != existing.ID || row.Issue.Title != "task" {
			t.Fatal("new task mapping/reference changed", row.Issue)
		}
	}
	if !strings.Contains(string(actual.TransferArchives[0].SourceEnvelope), "source-reviewer") {
		t.Fatal("original source history lost")
	}
}

func TestPreparedSQLiteWriteRejectsWrongProjectStaleTargetAndCapacity(t *testing.T) {
	for _, stage := range []string{"wrong_project", "stale", "capacity"} {
		t.Run(stage, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			database, err := db.Initialize(root)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = database.Close() }()
			target, err := InspectTarget(ctx, TargetSpec{ProjectDir: root, Store: "sqlite", Namespace: "target"}, 0)
			if err != nil {
				t.Fatal(err)
			}
			limit := int64(0)
			if stage == "capacity" {
				limit = 1
			}
			prepared, err := PrepareSQLiteWrite(githubTaskInputFixture(t), target, nil, limit)
			if stage == "capacity" {
				if err == nil || prepared != nil {
					t.Fatal("capacity allowed preparation")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if stage == "stale" {
				if _, err := database.CreateNote("external", "change"); err != nil {
					t.Fatal(err)
				}
			} else {
				other, err := db.Initialize(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = other.Close() }()
				if err := prepared.Apply(ctx, other); err == nil {
					t.Fatal("different project accepted")
				}
				return
			}
			if err := prepared.Apply(ctx, database); err == nil {
				t.Fatal("changed destination accepted")
			}
			actual, err := db.ReadSQLiteTransferDestination(ctx, root, 0)
			if err != nil {
				t.Fatal(err)
			}
			if len(actual.Tasks) != 0 || len(actual.TransferArchives) != 0 || len(actual.Notes) != 1 {
				t.Fatal("partial writes or external data loss")
			}
		})
	}
}

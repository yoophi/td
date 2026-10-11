package db

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/models"
)

func TestSQLiteWorkspaceSourceRetainsSharedDataWithoutChangingOriginal(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "source 한글")
	database, err := Initialize(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	parent := &models.Issue{Title: "Original parent", Status: models.StatusClosed, Acceptance: "Acceptance", Priority: models.PriorityP1}
	if err := database.CreateIssue(parent); err != nil {
		t.Fatal(err)
	}
	due := "2026-10-20"
	child := &models.Issue{Title: "Original child", ParentID: parent.ID, DueDate: &due, Description: "Original description"}
	if err := database.CreateIssue(child); err != nil {
		t.Fatal(err)
	}
	if err := database.AddDependency(child.ID, parent.ID, "depends_on"); err != nil {
		t.Fatal(err)
	}
	if err := database.LinkFile(child.ID, "src/example.go", models.FileRole("implementation"), "original-sha"); err != nil {
		t.Fatal(err)
	}
	if err := database.AddLog(&models.Log{IssueID: child.ID, SessionID: "original-worker", Message: "Original log", Type: models.LogTypeProgress}); err != nil {
		t.Fatal(err)
	}
	if err := database.AddHandoff(&models.Handoff{IssueID: child.ID, SessionID: "original-worker", Done: []string{"Original done"}}); err != nil {
		t.Fatal(err)
	}
	if err := database.AddComment(&models.Comment{IssueID: child.ID, SessionID: "original-peer", Text: "Original comment"}); err != nil {
		t.Fatal(err)
	}
	if err := database.AddGitSnapshot(&models.GitSnapshot{IssueID: child.ID, Event: "start", CommitSHA: "original-git-sha", Branch: "source-branch"}); err != nil {
		t.Fatal(err)
	}
	if err := database.RecordSessionAction(child.ID, "original-worker", models.IssueSessionAction("started")); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := database.conn.Exec(`INSERT INTO issue_reviews (id,issue_id,reviewer_session,decision,summary,created_at,self_review) VALUES ('original-review',?,'original-reviewer','approved','Original review',?,0)`, child.ID, now); err != nil {
		t.Fatal(err)
	}
	note, err := database.CreateNote("Deleted source note", "Original note")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.DeleteNote(note.ID); err != nil {
		t.Fatal(err)
	}
	board, err := database.CreateBoard("Source board", `id = "`+child.ID+`"`)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SetIssuePosition(board.ID, child.ID, 7); err != nil {
		t.Fatal(err)
	}
	if _, err := database.conn.Exec("UPDATE board_issue_positions SET deleted_at=? WHERE board_id=?", now.Format(time.RFC3339Nano), board.ID); err != nil {
		t.Fatal(err)
	}
	if err := database.DeleteIssue(child.ID); err != nil {
		t.Fatal(err)
	}
	if err := database.LogAction(&models.ActionLog{SessionID: "local-worker", EntityType: "work_sessions", EntityID: "private-work-bundle", ActionType: models.ActionUpdate, NewData: "PRIVATE_WORK_BUNDLE"}); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(dir, &models.Config{Store: config.StoreSQLite, FocusedIssueID: "PRIVATE_FOCUS"}); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	// The existing initializer treats '?' in a raw path as URI options. Move
	// the closed fixture first so this test exercises a real literal source
	// filename independently of that separate initialization defect.
	literalDir := filepath.Join(filepath.Dir(dir), "source ?# 한글")
	if err := os.Rename(dir, literalDir); err != nil {
		t.Fatal(err)
	}
	dir = literalDir
	path := filepath.Join(dir, dbFile)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := ReadSQLiteWorkspace(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("source database changed", err)
	}
	if len(workspace.Tasks) != 2 || len(workspace.Notes) != 1 || workspace.Notes[0].DeletedAt == nil || workspace.ExcludedActions["work_sessions"] != 1 {
		t.Fatal("source scope lost shared data or included local actions", workspace)
	}
	found := false
	for _, row := range workspace.Tasks {
		if row.Issue.ID != child.ID {
			continue
		}
		found = true
		if row.Issue.DeletedAt == nil || *row.Issue.DueDate != due || row.Issue.ParentID != parent.ID || len(row.Logs) != 1 || len(row.Handoffs) != 1 || len(row.Dependencies) != 1 || len(row.Files) != 1 || len(row.Comments) != 1 || len(row.Reviews) != 1 || len(row.SessionHistory) != 1 || len(row.GitSnapshots) != 1 {
			t.Fatal("incomplete source task", row)
		}
		if row.Logs[0].SessionID != "original-worker" || row.Handoffs[0].Done[0] != "Original done" || row.Reviews[0].ReviewerSession != "original-reviewer" || row.GitSnapshots[0].CommitSHA != "original-git-sha" || row.Dependencies[0].DependsOnID != parent.ID {
			t.Fatal("original history/attribution/reference changed", row)
		}
	}
	if !found {
		t.Fatal("deleted source task omitted")
	}
	found = false
	for _, row := range workspace.Boards {
		if row.Board.ID == board.ID {
			found = true
			if row.Board.LastViewedAt != nil || len(row.Positions) != 1 || row.Positions[0].DeletedAt == nil || row.Positions[0].Position != 7 || row.Positions[0].IssueID != child.ID || row.Positions[0].AddedAt.IsZero() {
				t.Fatal("position/tombstone lost", row)
			}
		}
	}
	if !found {
		t.Fatal("source board omitted")
	}
	data, err := json.Marshal(workspace)
	if err != nil || bytes.Contains(data, []byte("PRIVATE_FOCUS")) || bytes.Contains(data, []byte("PRIVATE_WORK_BUNDLE")) {
		t.Fatal("device-private data included", err)
	}
}

func TestSQLiteWorkspaceSourceReadHelpersShareTheReadTransaction(t *testing.T) {
	dir := t.TempDir()
	writer, err := Initialize(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Close() }()
	issue := &models.Issue{Title: "Original source title"}
	if err := writer.CreateIssue(issue); err != nil {
		t.Fatal(err)
	}
	// A fixture-only WAL writer can commit while the source observation remains
	// open, proving snapshot consistency rather than merely blocking a writer.
	if _, err := writer.conn.Exec("PRAGMA journal_mode=WAL"); err != nil {
		t.Fatal(err)
	}
	dsn := (&url.URL{Scheme: "file", Path: filepath.Join(dir, dbFile)}).String()
	conn, err := OpenSQLite(dsn, OpenOptions{ReadOnly: true, MaxOpenConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Exec("BEGIN"); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = conn.Exec("ROLLBACK") }()
	var version string
	if err := conn.QueryRow("SELECT value FROM schema_info WHERE key='version'").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.conn.Exec("UPDATE issues SET title='Concurrent new title' WHERE id=?", issue.ID); err != nil {
		t.Fatal(err)
	}
	reader := &DB{conn: conn, baseDir: dir}
	workspace, err := reader.readWorkspace(context.Background(), SchemaVersion)
	if err != nil || len(workspace.Tasks) != 1 || workspace.Tasks[0].Issue.Title != "Original source title" {
		t.Fatal("helper escaped source snapshot", workspace, err)
	}
	if _, err := conn.Exec("UPDATE issues SET title='Forbidden source write'"); err == nil {
		t.Fatal("source connection allowed writes")
	}
	actual, err := writer.GetIssue(issue.ID)
	if err != nil || actual.Title != "Concurrent new title" {
		t.Fatal("read-only source changed writer state", actual, err)
	}
}

func TestSQLiteWorkspaceSourceFailuresDoNotInitializeUpgradeOrFallback(t *testing.T) {
	for _, mode := range []string{"missing", "old schema", "missing logs", "invalid note clock", "GitHub selected", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			var before []byte
			if mode != "missing" && mode != "cancelled" {
				database, err := Initialize(dir)
				if err != nil {
					t.Fatal(err)
				}
				if err := database.CreateIssue(&models.Issue{Title: "Source"}); err != nil {
					t.Fatal(err)
				}
				if mode == "old schema" {
					if err := database.SetSchemaVersion(SchemaVersion - 1); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "missing logs" {
					if _, err := database.conn.Exec("DROP TABLE logs"); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "invalid note clock" {
					note, err := database.CreateNote("Source note", "body")
					if err != nil {
						t.Fatal(err)
					}
					if _, err := database.conn.Exec("UPDATE notes SET deleted_at='invalid original clock' WHERE id=?", note.ID); err != nil {
						t.Fatal(err)
					}
				}
				if err := database.Close(); err != nil {
					t.Fatal(err)
				}
				before, _ = os.ReadFile(filepath.Join(dir, dbFile))
			}
			if mode == "GitHub selected" {
				if err := config.Save(dir, &models.Config{Store: config.StoreGitHub}); err != nil {
					t.Fatal(err)
				}
			}
			ctx := context.Background()
			if mode == "cancelled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			result, err := ReadSQLiteWorkspace(ctx, dir)
			if result != nil || err == nil {
				t.Fatal("partial source/failure accepted", result, err)
			}
			if mode == "old schema" && !strings.Contains(err.Error(), "no migration attempted") || mode == "GitHub selected" && !strings.Contains(err.Error(), "no SQLite fallback") || mode == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatal("unclear source error", err)
			}
			after, readErr := os.ReadFile(filepath.Join(dir, dbFile))
			if before == nil && !os.IsNotExist(readErr) || before != nil && !bytes.Equal(before, after) {
				t.Fatal("failed source collection created/changed database", readErr)
			}
		})
	}
}

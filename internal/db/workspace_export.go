package db

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"time"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/transferlimit"
)

// SQLiteWorkspaceExport is shared source data, not active destination history.
// Device session registries, work bundles, credentials and configuration are
// deliberately absent. Unknown/local action kinds are reported by count.
type SQLiteWorkspaceExport struct {
	SchemaVersion   int                 `json:"schema_version"`
	Tasks           []SQLiteExportTask  `json:"tasks"`
	Notes           []models.Note       `json:"notes"`
	Boards          []SQLiteExportBoard `json:"boards"`
	Actions         []models.ActionLog  `json:"actions"`
	ExcludedActions map[string]int      `json:"excluded_actions"`
}

type SQLiteExportTask struct {
	Issue          models.Issue                 `json:"issue"`
	Logs           []models.Log                 `json:"logs"`
	Handoffs       []models.Handoff             `json:"handoffs"`
	Dependencies   []models.IssueDependency     `json:"dependencies"`
	Files          []models.IssueFile           `json:"files"`
	Comments       []models.Comment             `json:"comments"`
	Reviews        []*models.IssueReview        `json:"reviews"`
	SessionHistory []models.IssueSessionHistory `json:"session_history"`
	GitSnapshots   []models.GitSnapshot         `json:"git_snapshots"`
}

type SQLiteExportBoard struct {
	Board     models.Board           `json:"board"`
	Positions []SQLiteExportPosition `json:"positions"`
}

type SQLiteExportPosition struct {
	models.BoardIssue
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
}

// ReadSQLiteWorkspace never initializes or upgrades the source. A private
// one-connection, mode=ro handle keeps all existing read helpers inside the
// same explicitly opened SQLite read transaction. It is not exposed to callers
// or concurrent goroutines. Repository-wide GitHub observations have a separate
// best-effort boundary; this transaction only covers one SQLite database.
func ReadSQLiteWorkspace(ctx context.Context, baseDir string) (*SQLiteWorkspaceExport, error) {
	return ReadSQLiteWorkspaceLimited(ctx, baseDir, 0)
}

// ReadSQLiteWorkspaceLimited checks serialized fragments during collection.
// Existing table readers still materialize one table/entity at a time; this
// is an input-capacity guard, not a bound on SQLite driver's peak memory.
func ReadSQLiteWorkspaceLimited(ctx context.Context, baseDir string, maxBytes int64) (*SQLiteWorkspaceExport, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	budget, err := transferlimit.New(maxBytes)
	if err != nil {
		return nil, err
	}
	baseDir = ResolveBaseDir(baseDir)
	cfg, err := config.Load(baseDir)
	if err != nil {
		return nil, err
	}
	store, err := config.Store(cfg)
	if err != nil {
		return nil, err
	}
	if store != config.StoreSQLite {
		return nil, fmt.Errorf("SQLite source collection requires store=sqlite; no SQLite fallback attempted")
	}
	return readSQLiteWorkspaceReadOnly(ctx, baseDir, budget)
}

// ReadSQLiteTransferDestination inspects an explicitly selected SQLite target,
// regardless of the project's current store selection. It never initializes,
// migrates or selects SQLite as a fallback for a failed GitHub source/target.
func ReadSQLiteTransferDestination(ctx context.Context, baseDir string, maxBytes int64) (*SQLiteWorkspaceExport, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	budget, err := transferlimit.New(maxBytes)
	if err != nil {
		return nil, err
	}
	return readSQLiteWorkspaceReadOnly(ctx, ResolveBaseDir(baseDir), budget)
}

func readSQLiteWorkspaceReadOnly(ctx context.Context, baseDir string, budget *transferlimit.Budget) (*SQLiteWorkspaceExport, error) {
	path, err := filepath.Abs(filepath.Join(baseDir, dbFile))
	if err != nil {
		return nil, err
	}
	// Encode literal filenames (including ?/#/spaces) before appending URI options.
	dsn := (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
	conn, err := OpenSQLite(dsn, OpenOptions{ReadOnly: true, MaxOpenConns: 1})
	if err != nil {
		return nil, fmt.Errorf("open SQLite source read-only: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
		return nil, fmt.Errorf("begin SQLite source observation: %w", err)
	}
	defer func() { _, _ = conn.Exec("ROLLBACK") }()
	var rawVersion string
	if err := conn.QueryRowContext(ctx, "SELECT value FROM schema_info WHERE key='version'").Scan(&rawVersion); err != nil {
		return nil, fmt.Errorf("read SQLite source schema (source unchanged): %w", err)
	}
	version, err := strconv.Atoi(rawVersion)
	if err != nil || version != SchemaVersion {
		return nil, fmt.Errorf("SQLite source schema %q differs from required %d; use a compatible td version or explicitly upgrade an older source before transfer; no migration attempted", rawVersion, SchemaVersion)
	}
	database := &DB{conn: conn, baseDir: baseDir}
	return database.readWorkspaceLimited(ctx, version, budget)
}

func (db *DB) readWorkspace(ctx context.Context, version int) (*SQLiteWorkspaceExport, error) {
	budget, _ := transferlimit.New(0)
	return db.readWorkspaceLimited(ctx, version, budget)
}

func (db *DB) readWorkspaceLimited(ctx context.Context, version int, budget *transferlimit.Budget) (*SQLiteWorkspaceExport, error) {
	issues, err := db.ListIssues(ListIssuesOptions{IncludeDeleted: true})
	if err != nil {
		return nil, fmt.Errorf("read SQLite source tasks: %w", err)
	}
	result := &SQLiteWorkspaceExport{SchemaVersion: version, Tasks: []SQLiteExportTask{},
		Notes: []models.Note{}, Boards: []SQLiteExportBoard{}, Actions: []models.ActionLog{}, ExcludedActions: map[string]int{}}
	if err := budget.Add(result); err != nil {
		return nil, err
	}
	for _, issue := range issues {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		row := SQLiteExportTask{Issue: issue}
		if row.Logs, err = db.GetLogs(issue.ID, 0); err != nil {
			return nil, fmt.Errorf("source %s logs: %w", issue.ID, err)
		}
		if row.Handoffs, err = db.GetHandoffs(issue.ID); err != nil {
			return nil, fmt.Errorf("source %s handoffs: %w", issue.ID, err)
		}
		if row.Dependencies, err = db.GetIssueDependencyRelations(issue.ID); err != nil {
			return nil, fmt.Errorf("source %s dependencies: %w", issue.ID, err)
		}
		if row.Files, err = db.GetLinkedFiles(issue.ID); err != nil {
			return nil, fmt.Errorf("source %s files: %w", issue.ID, err)
		}
		if row.Comments, err = db.GetComments(issue.ID); err != nil {
			return nil, fmt.Errorf("source %s comments: %w", issue.ID, err)
		}
		if row.Reviews, err = db.ListIssueReviews(issue.ID); err != nil {
			return nil, fmt.Errorf("source %s reviews: %w", issue.ID, err)
		}
		if row.SessionHistory, err = db.GetSessionHistory(issue.ID); err != nil {
			return nil, fmt.Errorf("source %s attribution: %w", issue.ID, err)
		}
		if row.GitSnapshots, err = db.readWorkspaceSnapshots(ctx, issue.ID); err != nil {
			return nil, fmt.Errorf("source %s git snapshots: %w", issue.ID, err)
		}
		if err := budget.Add(row); err != nil {
			return nil, err
		}
		result.Tasks = append(result.Tasks, row)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if result.Notes, err = db.readWorkspaceNotes(ctx); err != nil {
		return nil, fmt.Errorf("read SQLite source notes: %w", err)
	}
	for _, note := range result.Notes {
		if err := budget.Add(note); err != nil {
			return nil, err
		}
	}
	boards, err := db.ListBoards()
	if err != nil {
		return nil, fmt.Errorf("read SQLite source boards: %w", err)
	}
	for _, board := range boards {
		positions, err := db.readWorkspacePositions(ctx, board.ID)
		if err != nil {
			return nil, fmt.Errorf("source board %s positions: %w", board.ID, err)
		}
		board.LastViewedAt = nil
		row := SQLiteExportBoard{Board: board, Positions: positions}
		if err := budget.Add(row); err != nil {
			return nil, err
		}
		result.Boards = append(result.Boards, row)
	}
	actions, err := db.GetRecentActionsAll(0)
	if err != nil {
		return nil, fmt.Errorf("read SQLite source actions: %w", err)
	}
	for _, action := range actions {
		switch action.EntityType {
		case "issue", "dependency", "file_link", "note", "board", "board_issue_positions",
			"logs", "comments", "handoffs", "issue_review", "issue_reviews", "git_snapshots", "issue_session_history":
			if err := budget.Add(action); err != nil {
				return nil, err
			}
			result.Actions = append(result.Actions, action)
		default:
			result.ExcludedActions[action.EntityType]++
		}
	}
	if err := budget.Add(result.ExcludedActions); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (db *DB) readWorkspaceNotes(ctx context.Context) ([]models.Note, error) {
	rows, err := db.conn.QueryContext(ctx, "SELECT id,title,content,created_at,updated_at,pinned,archived,deleted_at FROM notes ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := []models.Note{}
	for rows.Next() {
		var row models.Note
		var created, updated string
		var deleted sql.NullString
		if err := rows.Scan(&row.ID, &row.Title, &row.Content, &created, &updated, &row.Pinned, &row.Archived, &deleted); err != nil {
			return nil, err
		}
		row.CreatedAt, err = parseWorkspaceTime(created)
		if err != nil {
			return nil, fmt.Errorf("note %s creation clock: %w", row.ID, err)
		}
		row.UpdatedAt, err = parseWorkspaceTime(updated)
		if err != nil {
			return nil, fmt.Errorf("note %s update clock: %w", row.ID, err)
		}
		if deleted.Valid && deleted.String != "" {
			stamp, err := parseWorkspaceTime(deleted.String)
			if err != nil {
				return nil, fmt.Errorf("note %s deletion clock: %w", row.ID, err)
			}
			row.DeletedAt = &stamp
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (db *DB) readWorkspaceSnapshots(ctx context.Context, issueID string) ([]models.GitSnapshot, error) {
	rows, err := db.conn.QueryContext(ctx, "SELECT CAST(id AS TEXT), issue_id, event, commit_sha, branch, dirty_files, timestamp FROM git_snapshots WHERE issue_id=? ORDER BY timestamp,id", issueID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := []models.GitSnapshot{}
	for rows.Next() {
		var row models.GitSnapshot
		if err := rows.Scan(&row.ID, &row.IssueID, &row.Event, &row.CommitSHA, &row.Branch, &row.DirtyFiles, &row.Timestamp); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (db *DB) readWorkspacePositions(ctx context.Context, boardID string) ([]SQLiteExportPosition, error) {
	rows, err := db.conn.QueryContext(ctx, "SELECT board_id,issue_id,position,added_at,deleted_at FROM board_issue_positions WHERE board_id=? ORDER BY position,issue_id", boardID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := []SQLiteExportPosition{}
	for rows.Next() {
		var row SQLiteExportPosition
		var added string
		var deleted sql.NullString
		if err := rows.Scan(&row.BoardID, &row.IssueID, &row.Position, &added, &deleted); err != nil {
			return nil, err
		}
		row.AddedAt, err = parseWorkspaceTime(added)
		if err != nil {
			return nil, err
		}
		if deleted.Valid && deleted.String != "" {
			stamp, err := parseWorkspaceTime(deleted.String)
			if err != nil {
				return nil, err
			}
			row.DeletedAt = &stamp
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func parseWorkspaceTime(raw string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05.999999999 -0700 MST", time.DateTime} {
		if stamp, err := time.Parse(layout, raw); err == nil {
			return stamp, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid SQLite source timestamp %q", raw)
}

package db

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/marcus/td/internal/transferlimit"
)

// WorkspaceTransferArchive is passive provenance. It is never installed in
// action_log, issue_reviews or session history. The checked controller verifies
// the original envelope/checksum and receipts before calling this DB boundary.
type WorkspaceTransferArchive struct {
	SourceSHA256    string            `json:"source_sha256"`
	TargetNamespace string            `json:"target_namespace"`
	SourceEnvelope  json.RawMessage   `json:"source_envelope"`
	Mappings        map[string]string `json:"mappings"`
}

// OpenSQLiteTransferDestination explicitly opens an existing destination for
// writes, even if its configured store is GitHub. It never initializes or
// migrates, and mode=rw prevents a missing file from being silently created.
func OpenSQLiteTransferDestination(ctx context.Context, baseDir string) (*DB, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	baseDir = ResolveBaseDir(baseDir)
	path, err := filepath.Abs(filepath.Join(baseDir, dbFile))
	if err != nil {
		return nil, err
	}
	dsn := (&url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: "mode=rw"}).String()
	conn, err := OpenSQLite(dsn, OpenOptions{MaxOpenConns: 1})
	if err != nil {
		return nil, fmt.Errorf("open explicit SQLite transfer destination: %w", err)
	}
	var version string
	if err := conn.QueryRowContext(ctx, "SELECT value FROM schema_info WHERE key='version'").Scan(&version); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("read SQLite transfer destination schema: %w", err)
	}
	if version != fmt.Sprint(SchemaVersion) {
		_ = conn.Close()
		return nil, fmt.Errorf("SQLite transfer destination schema %q differs from required %d; no migration attempted", version, SchemaVersion)
	}
	return &DB{conn: conn, baseDir: baseDir}, nil
}

// ApplyWorkspaceTransfer inserts an entire prepared workspace and its source
// capsule in one SQLite transaction. It requires a private one-connection DB
// with the current schema, and does not initialize/migrate/select a backend.
// Existing entities are not overwritten; builtin board positions may be added
// only when they do not collide. Mapping/remote ownership belongs to the checked
// controller. Commit errors are uncertain: inspect state before any retry.
func (db *DB) ApplyWorkspaceTransfer(ctx context.Context, expected, incoming *SQLiteWorkspaceExport, archive WorkspaceTransferArchive, maxBytes int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if expected == nil || incoming == nil || expected.SchemaVersion != SchemaVersion || incoming.SchemaVersion != SchemaVersion {
		return fmt.Errorf("transfer requires current-schema source and destination rows")
	}
	if db.conn.Stats().MaxOpenConnections != 1 {
		return fmt.Errorf("SQLite transfer writer requires a private single-connection handle")
	}
	if _, err := hex.DecodeString(archive.SourceSHA256); err != nil || len(archive.SourceSHA256) != 64 || archive.TargetNamespace == "" || !json.Valid(archive.SourceEnvelope) {
		return fmt.Errorf("invalid passive source archive")
	}
	var header struct {
		SHA256 string `json:"sha256"`
	}
	if err := json.Unmarshal(archive.SourceEnvelope, &header); err != nil || header.SHA256 != archive.SourceSHA256 {
		return fmt.Errorf("passive archive source binding differs")
	}
	if len(incoming.Actions) > 0 || len(incoming.TransferArchives) > 0 {
		return fmt.Errorf("source action history and archives must remain in the passive source capsule")
	}
	for _, t := range incoming.Tasks {
		i := t.Issue
		if i.ID == "" || i.ID == "td-" || NormalizeIssueID(i.ID) != i.ID || i.CreatorSession != "" || i.ImplementerSession != "" || i.ReviewerSession != "" || i.ReviewRequestedBySession != "" || i.ClosedBySession != "" || i.ReviewedAt != nil || len(t.Reviews)+len(t.SessionHistory)+len(t.GitSnapshots)+len(t.Comments) > 0 {
			return fmt.Errorf("task %q contains unresolved IDs or active source provenance", i.ID)
		}
		for _, l := range t.Logs {
			if l.IssueID != i.ID || !strings.HasPrefix(l.SessionID, "imported-") || l.WorkSessionID != "" {
				return fmt.Errorf("task %q has active or foreign log ownership", i.ID)
			}
		}
		for _, h := range t.Handoffs {
			if h.IssueID != i.ID || !strings.HasPrefix(h.SessionID, "imported-") {
				return fmt.Errorf("task %q has active or foreign handoff ownership", i.ID)
			}
		}
		for _, f := range t.Files {
			if f.ID == "" || f.IssueID != i.ID {
				return fmt.Errorf("task %q has unresolved file ownership", i.ID)
			}
		}
		for _, d := range t.Dependencies {
			if d.IssueID != i.ID {
				return fmt.Errorf("task %q has foreign dependency ownership", i.ID)
			}
		}
	}
	budget, err := transferlimit.New(maxBytes)
	if err != nil {
		return err
	}
	if err := budget.Add(incoming); err != nil {
		return err
	}
	if err := budget.Add(archive); err != nil {
		return err
	}
	basis, err := json.Marshal(expected)
	if err != nil {
		return err
	}
	mappings, err := json.Marshal(archive.Mappings)
	if err != nil {
		return err
	}
	return db.withWriteLock(func() error {
		if _, err := db.conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
			return fmt.Errorf("begin SQLite transfer: %w", err)
		}
		defer func() { _, _ = db.conn.Exec("ROLLBACK") }()
		var version string
		if err := db.conn.QueryRowContext(ctx, "SELECT value FROM schema_info WHERE key='version'").Scan(&version); err != nil {
			return fmt.Errorf("read SQLite destination schema: %w", err)
		}
		if version != fmt.Sprint(SchemaVersion) {
			return fmt.Errorf("SQLite destination schema changed: %q", version)
		}
		current, err := db.readWorkspaceLimited(ctx, SchemaVersion, budgetForTransfer(maxBytes))
		if err != nil {
			return err
		}
		actual, err := json.Marshal(current)
		if err != nil {
			return err
		}
		if !bytes.Equal(basis, actual) {
			return fmt.Errorf("SQLite destination changed since observation; no transfer rows committed")
		}
		if _, err := db.conn.ExecContext(ctx, "PRAGMA defer_foreign_keys = ON"); err != nil {
			return err
		}
		// Transactional DDL rolls back with failed row writes. Older databases
		// need no migration just to inspect a source or preview a transfer.
		if _, err := db.conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS store_transfer_archives (source_sha256 TEXT PRIMARY KEY, target_namespace TEXT NOT NULL, source_envelope BLOB NOT NULL, mappings BLOB NOT NULL)`); err != nil {
			return err
		}
		if _, err := db.conn.ExecContext(ctx, `INSERT INTO store_transfer_archives(source_sha256,target_namespace,source_envelope,mappings) VALUES (?,?,?,?)`, archive.SourceSHA256, archive.TargetNamespace, []byte(archive.SourceEnvelope), mappings); err != nil {
			return fmt.Errorf("store passive transfer archive (inspect existing receipts before retry): %w", err)
		}
		existing := map[string]bool{}
		for _, t := range current.Tasks {
			existing[t.Issue.ID] = true
		}
		for _, t := range incoming.Tasks {
			if existing[t.Issue.ID] {
				return fmt.Errorf("SQLite task ID collision %q", t.Issue.ID)
			}
			existing[t.Issue.ID] = true
		}
		for _, t := range incoming.Tasks {
			if t.Issue.ParentID != "" && !existing[t.Issue.ParentID] {
				return fmt.Errorf("unresolved SQLite parent %q", t.Issue.ParentID)
			}
		}
		inserter := transferInsertExecer{ctx, db.conn}
		for _, t := range incoming.Tasks {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := upsertIssueExec(inserter, &t.Issue); err != nil {
				return err
			}
			for _, l := range t.Logs {
				if err := insertLogExec(inserter, &l); err != nil {
					return err
				}
			}
			for _, h := range t.Handoffs {
				if err := insertHandoffExec(inserter, &h); err != nil {
					return err
				}
			}
			for _, f := range t.Files {
				if err := insertIssueFileExec(inserter, &f); err != nil {
					return err
				}
			}
			for _, d := range t.Dependencies {
				if _, err := db.conn.ExecContext(ctx, `INSERT INTO issue_dependencies(id,issue_id,depends_on_id,relation_type) VALUES (?,?,?,?)`, DependencyID(t.Issue.ID, d.DependsOnID, d.RelationType), t.Issue.ID, d.DependsOnID, d.RelationType); err != nil {
					return err
				}
			}
		}
		for _, n := range incoming.Notes {
			var deleted any
			if n.DeletedAt != nil {
				deleted = n.DeletedAt.Format(time.RFC3339Nano)
			}
			if _, err := db.conn.ExecContext(ctx, `INSERT INTO notes(id,title,content,created_at,updated_at,pinned,archived,deleted_at) VALUES (?,?,?,?,?,?,?,?)`, n.ID, n.Title, n.Content, n.CreatedAt.Format(time.RFC3339Nano), n.UpdatedAt.Format(time.RFC3339Nano), n.Pinned, n.Archived, deleted); err != nil {
				return fmt.Errorf("insert transfer note %q: %w", n.ID, err)
			}
		}
		for _, b := range incoming.Boards {
			if b.Board.LastViewedAt != nil {
				return fmt.Errorf("device board view state cannot be imported")
			}
			if err := parseAndValidateQuery(b.Board.Query); err != nil {
				return err
			}
			if b.Board.IsBuiltin {
				var builtin bool
				if b.Board.ID != "bd-all-issues" || b.Board.Query != "" {
					return fmt.Errorf("invalid builtin board transfer")
				}
				if err := db.conn.QueryRowContext(ctx, "SELECT is_builtin FROM boards WHERE id=?", b.Board.ID).Scan(&builtin); err != nil {
					return fmt.Errorf("read destination builtin board: %w", err)
				}
				if !builtin {
					return fmt.Errorf("destination builtin board replaced")
				}
			} else if _, err := db.conn.ExecContext(ctx, `INSERT INTO boards(id,name,query,is_builtin,view_mode,created_at,updated_at) VALUES (?,?,?,0,?,?,?)`, b.Board.ID, b.Board.Name, b.Board.Query, b.Board.ViewMode, b.Board.CreatedAt, b.Board.UpdatedAt); err != nil {
				return fmt.Errorf("insert transfer board %q: %w", b.Board.ID, err)
			}
			for _, p := range b.Positions {
				if p.BoardID != b.Board.ID {
					return fmt.Errorf("foreign board position ownership")
				}
				if p.DeletedAt == nil {
					var occupied int
					if err := db.conn.QueryRowContext(ctx, "SELECT count(*) FROM board_issue_positions WHERE board_id=? AND position=? AND deleted_at IS NULL", p.BoardID, p.Position).Scan(&occupied); err != nil {
						return err
					}
					if occupied != 0 {
						return fmt.Errorf("SQLite board position collision at %q/%d", p.BoardID, p.Position)
					}
				}
				if _, err := db.conn.ExecContext(ctx, `INSERT INTO board_issue_positions(id,board_id,issue_id,position,added_at,deleted_at) VALUES (?,?,?,?,?,?)`, BoardIssuePosID(p.BoardID, p.IssueID), p.BoardID, p.IssueID, p.Position, p.AddedAt, p.DeletedAt); err != nil {
					return err
				}
			}
		}
		if _, err := db.conn.ExecContext(ctx, "COMMIT"); err != nil {
			return fmt.Errorf("commit SQLite transfer failed; outcome may be uncertain; inspect archive and destination before retry: %w", err)
		}
		return nil
	})
}

func budgetForTransfer(limit int64) *transferlimit.Budget { b, _ := transferlimit.New(limit); return b }

// Reuse fixed internal import SQL while requiring INSERT conflicts to fail.
// Transfer must never silently ignore a row or replace an existing entity.
type transferInsertExecer struct {
	ctx  context.Context
	conn *sql.DB
}

func (e transferInsertExecer) Exec(query string, args ...any) (sql.Result, error) {
	query = strings.Replace(query, "INSERT OR IGNORE", "INSERT", 1)
	query = strings.Replace(query, "INSERT OR REPLACE", "INSERT", 1)
	return e.conn.ExecContext(e.ctx, query, args...)
}

func (db *DB) readTransferArchives(ctx context.Context, budget *transferlimit.Budget) ([]WorkspaceTransferArchive, error) {
	var exists int
	if err := db.conn.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='store_transfer_archives'").Scan(&exists); err != nil {
		return nil, err
	}
	if exists == 0 {
		return nil, nil
	}
	rows, err := db.conn.QueryContext(ctx, "SELECT source_sha256,target_namespace,source_envelope,mappings FROM store_transfer_archives ORDER BY source_sha256")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := []WorkspaceTransferArchive{}
	for rows.Next() {
		var a WorkspaceTransferArchive
		var mappings []byte
		if err := rows.Scan(&a.SourceSHA256, &a.TargetNamespace, &a.SourceEnvelope, &mappings); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(mappings, &a.Mappings); err != nil {
			return nil, fmt.Errorf("invalid transfer archive mappings: %w", err)
		}
		if err := budget.Add(a); err != nil {
			return nil, err
		}
		result = append(result, a)
	}
	return result, rows.Err()
}

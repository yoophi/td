package storetransfer

import (
	"context"
	"fmt"

	"github.com/marcus/td/internal/db"
)

// SQLiteTransferResult describes a committed transaction, not a portable
// checkpoint or GitHub execution receipt. The same source capsule/mappings are
// stored in the destination archive transactionally with the inserted entities.
type SQLiteTransferResult struct {
	DryRun          bool              `json:"dry_run"`
	SourceStore     string            `json:"source_store"`
	TargetStore     string            `json:"target_store"`
	Committed       bool              `json:"committed"`
	SourceSHA256    string            `json:"source_sha256"`
	TargetNamespace string            `json:"target_namespace"`
	Initialized     bool              `json:"initialized"`
	Tasks           int               `json:"inserted_tasks"`
	Notes           int               `json:"inserted_notes"`
	Boards          int               `json:"inserted_boards"`
	Positions       int               `json:"inserted_positions"`
	Mappings        map[string]string `json:"mappings"`
	Preservation    []Disclosure      `json:"preservation"`
}

// ExecuteSQLiteDestination runs one explicit prepared transfer. It never retries
// a write or changes store configuration. Errors retain their original cause;
// an initialized destination or uncertain commit must be inspected before retry.
func ExecuteSQLiteDestination(ctx context.Context, source *Source, target *InspectedTarget, bindings map[string]string, maxBytes int64) (*SQLiteTransferResult, error) {
	prepared, err := PrepareSQLiteDestination(ctx, source, target, bindings, maxBytes)
	if err != nil {
		return nil, err
	}
	writer, err := db.OpenSQLiteTransferDestination(ctx, prepared.root)
	if err != nil {
		return nil, fmt.Errorf("open SQLite transfer writer (initialization may have completed; inspect before retry): %w", err)
	}
	defer func() { _ = writer.Close() }()
	if err := prepared.Apply(ctx, writer); err != nil {
		return nil, fmt.Errorf("SQLite transfer failed (source_sha256=%s; inspect destination/archive before retry; no automatic retry): %w", prepared.archive.SourceSHA256, err)
	}
	result := &SQLiteTransferResult{SourceStore: "gh-issue", TargetStore: "sqlite", Committed: true, SourceSHA256: prepared.archive.SourceSHA256, TargetNamespace: prepared.archive.TargetNamespace,
		Initialized: target.NeedsSQLiteInitialization, Tasks: len(prepared.rows.Tasks), Notes: len(prepared.rows.Notes), Mappings: prepared.Mappings()}
	for _, board := range prepared.rows.Boards {
		if !board.Board.IsBuiltin {
			result.Boards++
		}
		result.Positions += len(board.Positions)
	}
	if err := writer.Close(); err != nil {
		return result, fmt.Errorf("SQLite transfer committed (source_sha256=%s) but destination close failed; inspect archive before retry: %w", result.SourceSHA256, err)
	}
	return result, nil
}

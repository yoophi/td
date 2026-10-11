package storetransfer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path/filepath"

	"github.com/marcus/td/internal/db"
	"github.com/marcus/td/internal/transferlimit"
)

// PreparedSQLiteWrite owns the validated, frozen input and destination basis.
// Its private rows cannot be replaced by caller-supplied projections or maps.
// It is not a persistent checkpoint or proof of a completed transaction.
type PreparedSQLiteWrite struct {
	root     string
	expected *db.SQLiteWorkspaceExport
	rows     *db.SQLiteWorkspaceExport
	archive  db.WorkspaceTransferArchive
	limit    int64
}

// PrepareSQLiteDestination performs projection/capacity checks before creating
// a new database. Existing targets use their original observation unchanged.
// Initialization returns its private staging baseline, rather than accepting
// arbitrary edits observed after publication. Apply still checks that baseline
// while holding the destination write transaction.
func PrepareSQLiteDestination(ctx context.Context, source *Source, target *InspectedTarget, bindings map[string]string, maxBytes int64) (*PreparedSQLiteWrite, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if target == nil {
		return nil, fmt.Errorf("explicit SQLite destination required")
	}
	if !target.NeedsSQLiteInitialization {
		return PrepareSQLiteWrite(source, target, bindings, maxBytes)
	}
	copyTarget := *target
	copyTarget.NeedsSQLiteInitialization = false
	prepared, err := PrepareSQLiteWrite(source, &copyTarget, bindings, maxBytes)
	if err != nil {
		return nil, err
	}
	if len(prepared.expected.Tasks)+len(prepared.expected.Notes)+len(prepared.expected.Boards)+len(prepared.expected.Actions)+len(prepared.expected.TransferArchives)+len(prepared.expected.ExcludedActions) != 0 {
		return nil, fmt.Errorf("new SQLite destination observation is not empty; no initialization attempted")
	}
	// Match the DB boundary budget before any directories/schema are created.
	budget, err := transferlimit.New(maxBytes)
	if err != nil {
		return nil, err
	}
	if err := budget.Add(prepared.rows); err != nil {
		return nil, err
	}
	if err := budget.Add(prepared.archive); err != nil {
		return nil, err
	}
	baseline, err := db.InitializeSQLiteTransferDestination(ctx, prepared.root)
	if err != nil {
		return nil, fmt.Errorf("initialize explicit SQLite destination: %w", err)
	}
	prepared.expected = baseline
	return prepared, nil
}

// PrepareSQLiteWrite allocates fresh local IDs once and resolves every active
// reference before opening a writer. Explicit adoption reuses the destination
// entity unchanged; the original source content remains in the passive capsule.
// Builtin board positions may be added, subject to transactional collision checks.
func PrepareSQLiteWrite(source *Source, target *InspectedTarget, bindings map[string]string, maxBytes int64) (*PreparedSQLiteWrite, error) {
	if target == nil || target.Observation == nil || target.Spec.ProjectDir == "" || target.Spec.Remote != "" || target.Spec.Store != "sqlite" || target.Spec.Namespace != target.Observation.Envelope.Namespace {
		return nil, fmt.Errorf("validated explicit SQLite destination required")
	}
	if target.NeedsSQLiteInitialization {
		return nil, fmt.Errorf("SQLite destination initialization must be completed and observed before preparing rows")
	}
	root, err := filepath.Abs(db.ResolveBaseDir(target.Spec.ProjectDir))
	if err != nil {
		return nil, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	observed, err := target.Observation.Envelope.decode()
	if err != nil {
		return nil, err
	}
	if observed.SQLite == nil {
		return nil, fmt.Errorf("SQLite destination observation required")
	}
	plan, err := BuildDataPlan(source, observed, bindings)
	if err != nil {
		return nil, err
	}
	used := map[string]bool{}
	preview, err := observed.Preview()
	if err != nil {
		return nil, err
	}
	for _, i := range preview.Identities {
		used[i.ID] = true
	}
	mappings := map[string]string{}
	adopted := map[string]bool{}
	for _, a := range plan.Identities.Assignments {
		if a.Action == "archive" {
			continue
		}
		id := a.TargetID
		if a.Action == "create" {
			prefix := map[string]string{"task": "td-", "note": "nt-", "board": "bd-"}[a.Kind]
			if prefix == "" {
				return nil, fmt.Errorf("unsupported SQLite entity kind %q", a.Kind)
			}
			var nonce [8]byte
			if _, err := rand.Read(nonce[:]); err != nil {
				return nil, fmt.Errorf("allocate SQLite identity: %w", err)
			}
			id = prefix + hex.EncodeToString(nonce[:])
			if used[id] {
				return nil, fmt.Errorf("allocated SQLite identity collision %q; prepare a new plan", id)
			}
			used[id] = true
		}
		if a.Action == "adopt" {
			adopted[id] = true
		}
		mappings[a.SourceKey] = id
	}
	rows, err := projectSQLiteWorkspace(source, mappings, adopted)
	if err != nil {
		return nil, err
	}
	// Adoption is explicit reuse, not overwrite. New entities may reference an
	// adopted task, but no source history/content is installed over that entity.
	tasks := rows.Tasks[:0]
	for _, row := range rows.Tasks {
		if !adopted[row.Issue.ID] {
			tasks = append(tasks, row)
		}
	}
	rows.Tasks = tasks
	notes := rows.Notes[:0]
	for _, row := range rows.Notes {
		if !adopted[row.ID] {
			notes = append(notes, row)
		}
	}
	rows.Notes = notes
	boards := rows.Boards[:0]
	for _, row := range rows.Boards {
		if !adopted[row.Board.ID] {
			boards = append(boards, row)
		}
	}
	rows.Boards = boards
	archive, err := SQLiteSourceArchive(source, observed.Envelope.Namespace, mappings, maxBytes)
	if err != nil {
		return nil, err
	}
	return &PreparedSQLiteWrite{root, observed.SQLite, rows, archive, maxBytes}, nil
}

// Mappings returns an informational copy. Editing it does not change the write.
func (p *PreparedSQLiteWrite) Mappings() map[string]string {
	result := map[string]string{}
	if p != nil {
		for key, id := range p.archive.Mappings {
			result[key] = id
		}
	}
	return result
}

// Apply requires a private explicit SQLite handle for the prepared project.
// DB verifies destination freshness and commits the capsule and rows together.
// The CLI controller must still handle initialization, receipts and journaling.
func (p *PreparedSQLiteWrite) Apply(ctx context.Context, database *db.DB) error {
	if p == nil || database == nil {
		return fmt.Errorf("prepared SQLite write and destination handle required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := filepath.Abs(db.ResolveBaseDir(database.BaseDir()))
	if err != nil {
		return err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	if root != p.root {
		return fmt.Errorf("SQLite writer project differs from prepared destination")
	}
	return database.ApplyWorkspaceTransfer(ctx, p.expected, p.rows, p.archive, p.limit)
}

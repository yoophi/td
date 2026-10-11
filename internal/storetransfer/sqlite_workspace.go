package storetransfer

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/marcus/td/internal/db"
	"github.com/marcus/td/internal/models"
)

// ProjectSQLiteWorkspace prepares current entities. Deleted GitHub boards have
// no SQLite tombstone column: keep their configuration/positions solely in the
// complete passive capsule, rather than recreate them as live boards.
// Mapping ownership and destination freshness must be checked by the executor.
func ProjectSQLiteWorkspace(source *Source, targetIDs map[string]string) (*db.SQLiteWorkspaceExport, error) {
	return projectSQLiteWorkspace(source, targetIDs, nil)
}

func projectSQLiteWorkspace(source *Source, targetIDs map[string]string, adopted map[string]bool) (*db.SQLiteWorkspaceExport, error) {
	if source == nil {
		return nil, fmt.Errorf("source is required")
	}
	s, err := source.Envelope.decode()
	if err != nil {
		return nil, err
	}
	if s.GitHub == nil {
		return nil, fmt.Errorf("SQLite workspace projection requires a GitHub source")
	}
	preview, err := s.Preview()
	if err != nil {
		return nil, err
	}
	keys := map[string]string{}
	taskIDs := map[string]string{}
	known := map[string]bool{}
	for _, i := range preview.Identities {
		keys[i.ID] = i.Key
		known[i.Key] = true
		if i.Kind == "task" {
			taskIDs[i.Key] = targetIDs[i.Key]
		}
	}
	for key := range targetIDs {
		if !known[key] {
			return nil, fmt.Errorf("unknown workspace mapping %q", key)
		}
	}
	tasks, err := ProjectSQLiteTasks(s, taskIDs)
	if err != nil {
		return nil, err
	}
	result := &db.SQLiteWorkspaceExport{SchemaVersion: db.SchemaVersion, Tasks: tasks}
	used := map[string]bool{}
	for _, id := range taskIDs {
		used[id] = true
	}
	resolve := func(sourceID, prefix string) (string, error) {
		id := targetIDs[keys[sourceID]]
		if id == "" || !strings.HasPrefix(id, prefix) || id == prefix || strings.TrimSpace(id) != id || strings.ContainsAny(id, "\r\n\x00") || used[id] {
			return "", fmt.Errorf("missing, invalid or duplicate workspace mapping for %q", sourceID)
		}
		used[id] = true
		return id, nil
	}
	for _, note := range s.GitHub.Notes {
		id, err := resolve(note.ID, "nt-")
		if err != nil {
			return nil, err
		}
		n := note.Note
		n.ID = id
		result.Notes = append(result.Notes, n)
	}
	queryIDs := map[string]string{}
	for _, task := range s.GitHub.Tasks {
		queryIDs[task.Record.ID] = targetIDs[keys[task.Record.ID]]
	}
	for _, board := range s.GitHub.Boards {
		if board.Details.DeletedAt != nil {
			if targetIDs[keys[board.ID]] != "" {
				return nil, fmt.Errorf("deleted GitHub board %q cannot map to a live SQLite board", board.ID)
			}
			continue
		}
		id, err := resolve(board.ID, "bd-")
		if err != nil {
			return nil, err
		}
		if board.IsBuiltin && id != "bd-all-issues" || !board.IsBuiltin && id == "bd-all-issues" {
			return nil, fmt.Errorf("builtin board mapping differs")
		}
		if adopted[id] {
			continue
		}
		q, err := RewriteQuery(board.Query, queryIDs)
		if err != nil {
			return nil, err
		}
		if q.Blocked() {
			return nil, fmt.Errorf("board %q has unresolved or unsupported query references", board.ID)
		}
		b := board.Board
		b.ID = id
		b.Query = q.Query
		b.LastViewedAt = nil
		row := db.SQLiteExportBoard{Board: b}
		for _, p := range board.Details.Positions {
			target := queryIDs[p.IssueID]
			if target == "" {
				return nil, fmt.Errorf("board %q has unresolved position", board.ID)
			}
			row.Positions = append(row.Positions, db.SQLiteExportPosition{BoardIssue: models.BoardIssue{BoardID: id, IssueID: target, Position: p.Position, AddedAt: p.AddedAt}})
		}
		result.Boards = append(result.Boards, row)
	}
	return result, nil
}

// SQLiteSourceArchive preserves original bytes and mappings without converting
// historical reviews/actions to active rows. The DB writer stores it atomically
// with entity rows; it is an archive, not proof of receipt/ownership authority.
func SQLiteSourceArchive(source *Source, targetNamespace string, mappings map[string]string, maxBytes int64) (db.WorkspaceTransferArchive, error) {
	var data bytes.Buffer
	if err := source.Write(&data, maxBytes); err != nil {
		return db.WorkspaceTransferArchive{}, err
	}
	copyMap := map[string]string{}
	for key, id := range mappings {
		copyMap[key] = id
	}
	return db.WorkspaceTransferArchive{SourceSHA256: source.Envelope.SHA256, TargetNamespace: targetNamespace, SourceEnvelope: data.Bytes(), Mappings: copyMap}, nil
}

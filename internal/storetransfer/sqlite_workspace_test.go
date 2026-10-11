package storetransfer

import (
	"context"
	"testing"
	"time"

	"github.com/marcus/td/internal/db"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
)

func TestSQLiteWorkspaceProjectionAtomicWriteAndArchiveRecollection(t *testing.T) {
	s := githubTaskInputFixture(t)
	now := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	s.GitHub.Notes = []ghstore.NoteRecord{{Note: models.Note{ID: "nt-gh-5", Title: "note", Content: "original", Pinned: true, Archived: true, DeletedAt: &now, CreatedAt: now, UpdatedAt: now}, Number: 5}}
	s.GitHub.Native = append(s.GitHub.Native, ghstore.NativeExport{Number: 5, Kind: "note", Title: "note"})
	for _, n := range []int{3, 4} {
		id, name := "bd-gh-3", "active"
		var deleted *time.Time
		if n == 4 {
			id, name, deleted = "bd-gh-4", "deleted", &now
		}
		b := ghstore.BoardRecord{Board: models.Board{ID: id, Name: name, Query: `id = "gh-1"`, ViewMode: "backlog", CreatedAt: now, UpdatedAt: now}, Number: n, Details: ghstore.BoardDetails{Version: 1, Query: `id = "gh-1"`, ViewMode: "backlog", DeletedAt: deleted, Positions: []ghstore.BoardPosition{{IssueID: "gh-1", Position: 0, AddedAt: now}}}}
		s.GitHub.Boards = append(s.GitHub.Boards, b)
		s.GitHub.Native = append(s.GitHub.Native, ghstore.NativeExport{Number: n, Kind: "board", Title: name})
	}
	source, err := NewGitHub(s.GitHub, 0)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	database, err := db.Initialize(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	database.SetMaxOpenConns(1)
	expected, err := db.ReadSQLiteTransferDestination(context.Background(), root, 0)
	if err != nil {
		t.Fatal(err)
	}
	target, err := NewSQLite("target", expected, 0)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildDataPlan(source, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	mappings := map[string]string{}
	for _, a := range plan.Identities.Assignments {
		switch a.SourceID {
		case "gh-1":
			mappings[a.SourceKey] = "td-first"
		case "gh-2":
			mappings[a.SourceKey] = "td-second"
		case "nt-gh-5":
			mappings[a.SourceKey] = "nt-new"
		case "bd-gh-3":
			mappings[a.SourceKey] = "bd-new"
		case "bd-gh-4":
			if a.Action != "archive" || a.TargetID != "" {
				t.Fatal("deleted board became a live assignment")
			}
		}
	}
	if plan.BoardQueries[1].Handling != "passive_archive" || plan.Positions[1].Handling != "passive_archive" {
		t.Fatal("deleted board configuration/positions activated")
	}
	for _, d := range plan.Disclosures {
		if d.Code == "boards_configuration_queries" && d.Count != 1 {
			t.Fatal("archived board counted as live configuration")
		}
	}
	projected, err := ProjectSQLiteWorkspace(source, mappings)
	if err != nil {
		t.Fatal(err)
	}
	if len(projected.Boards) != 1 || projected.Boards[0].Board.Query != `id = "td-first"` || projected.Boards[0].Positions[0].IssueID != "td-first" {
		t.Fatal("live board mapping incorrect")
	}
	archive, err := SQLiteSourceArchive(source, "target", mappings, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.ApplyWorkspaceTransfer(context.Background(), expected, projected, archive, 0); err != nil {
		t.Fatal(err)
	}
	actual, err := db.ReadSQLiteTransferDestination(context.Background(), root, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(actual.TransferArchives) != 1 || string(actual.TransferArchives[0].SourceEnvelope) != string(archive.SourceEnvelope) || len(actual.Notes) != 1 || actual.Notes[0].DeletedAt == nil {
		t.Fatal("original capsule or note flags lost")
	}
	// A subsequent full source capture includes the previous passive capsule.
	if _, err := NewSQLite("target", actual, 0); err != nil {
		t.Fatal("archive broke subsequent source collection", err)
	}
	preview, err := source.Preview()
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range preview.Identities {
		if i.ID == "bd-gh-4" {
			mappings[i.Key] = "bd-illegal"
		}
	}
	if rows, err := ProjectSQLiteWorkspace(source, mappings); err == nil || rows != nil {
		t.Fatal("deleted board mapped to live SQL board")
	}
}

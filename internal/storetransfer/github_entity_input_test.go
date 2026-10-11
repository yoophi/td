package storetransfer

import (
	"testing"
	"time"

	"github.com/marcus/td/internal/db"
	"github.com/marcus/td/internal/models"
)

func githubEntitySourceFixture(t *testing.T) (*Source, map[string]string) {
	t.Helper()
	now := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	data := &db.SQLiteWorkspaceExport{SchemaVersion: db.SchemaVersion, Tasks: []db.SQLiteExportTask{{Issue: models.Issue{ID: "td-parent", Title: "parent", Type: models.TypeTask, Priority: models.PriorityP2, Status: models.StatusOpen}}, {Issue: models.Issue{ID: "td-child", Title: "child", Type: models.TypeTask, Priority: models.PriorityP2, Status: models.StatusOpen, ParentID: "td-parent", CreatorSession: "original"}}}, Notes: []models.Note{{ID: "nt-one", Title: "source note", Content: "original content", Pinned: true, Archived: true, DeletedAt: &now, CreatedAt: now, UpdatedAt: now}}, Boards: []db.SQLiteExportBoard{{Board: models.Board{ID: "bd-one", Name: "source board", Query: `id = "td-child"`, ViewMode: "backlog", CreatedAt: now, UpdatedAt: now}, Positions: []db.SQLiteExportPosition{{BoardIssue: models.BoardIssue{BoardID: "bd-one", IssueID: "td-child", Position: 4, AddedAt: now}}, {BoardIssue: models.BoardIssue{BoardID: "bd-one", IssueID: "td-parent", Position: 0, AddedAt: now}, DeletedAt: &now}}}}}
	s, err := NewSQLite("original", data, 0)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := s.Preview()
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, i := range preview.Identities {
		if i.ID == "td-parent" {
			ids[i.Key] = "gh-11"
		}
		if i.ID == "td-child" {
			ids[i.Key] = "gh-22"
		}
	}
	return s, ids
}

func TestGitHubEntityInputsUseFrozenSourceMapQueryAndKeepPositionTombstonesPassive(t *testing.T) {
	s, ids := githubEntitySourceFixture(t)
	s.SQLite.Notes[0].Pinned = false
	s.SQLite.Boards[0].Board.Query = "tampered typed source"
	input, err := BuildGitHubEntityInputs(s, ids, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(input.Notes) != 1 || !input.Notes[0].Options.Pinned || !input.Notes[0].Options.Archived || !input.Notes[0].Options.Deleted || input.Notes[0].Content != "original content" {
		t.Fatal("note flags/content lost")
	}
	if input.Notes[0].Options.DeletedAt == nil || input.Notes[0].Options.DeletedAt.IsZero() {
		t.Fatal("portable deletion clock lost")
	}
	if len(input.Boards) != 1 || input.Boards[0].Query != `id = "gh-22"` || input.Boards[0].Options.ViewMode != "backlog" || len(input.Boards[0].Options.Positions) != 1 || input.Boards[0].Options.Positions[0].IssueID != "gh-22" || input.Boards[0].Options.Positions[0].Position != 4 {
		t.Fatal("query/membership mapping lost or tombstone activated")
	}
	if input.SourceSHA256 != s.Envelope.SHA256 {
		t.Fatal("input capsule binding differs")
	}
}

func TestGitHubEntityInputsRejectInvalidMappingsNamesQueriesAndSlotsWithoutPartialInput(t *testing.T) {
	for _, stage := range []string{"unknown", "missing", "duplicate_binding", "noncanonical", "duplicate_names", "query_pattern", "slot_collision", "normalized_title"} {
		t.Run(stage, func(t *testing.T) {
			s, ids := githubEntitySourceFixture(t)
			switch stage {
			case "unknown":
				ids["unknown"] = "gh-30"
			case "missing":
				for key := range ids {
					delete(ids, key)
					break
				}
			case "duplicate_binding":
				for key := range ids {
					ids[key] = "gh-11"
				}
			case "noncanonical":
				for key := range ids {
					ids[key] = "gh-011"
					break
				}
			case "duplicate_names":
				b := s.SQLite.Boards[0]
				b.Board.ID = "bd-other"
				b.Board.Name = "SOURCE BOARD"
				b.Positions = nil
				s.SQLite.Boards = append(s.SQLite.Boards, b)
			case "query_pattern":
				s.SQLite.Boards[0].Board.Query = `id ~ "td-*"`
			case "slot_collision":
				s.SQLite.Boards[0].Positions[1].DeletedAt = nil
				s.SQLite.Boards[0].Positions[1].Position = 4
			case "normalized_title":
				s.SQLite.Notes[0].Title = " padded "
			}
			if stage == "duplicate_names" || stage == "query_pattern" || stage == "slot_collision" || stage == "normalized_title" {
				var err error
				s, err = NewSQLite("original", s.SQLite, 0)
				if err != nil {
					t.Fatal(err)
				}
			}
			if input, err := BuildGitHubEntityInputs(s, ids, nil); err == nil || input != nil {
				t.Fatal("invalid projection returned partial input")
			}
		})
	}
}

func TestGitHubEntityInputsOmitExplicitlyReusedNonWrittenEntities(t *testing.T) {
	s, ids := githubEntitySourceFixture(t)
	s.SQLite.Notes[0].Title = ""
	s.SQLite.Boards[0].Board.Query = `id ~ "td-*"`
	var err error
	s, err = NewSQLite("original", s.SQLite, 0)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Preview()
	if err != nil {
		t.Fatal(err)
	}
	reused := map[string]bool{}
	for _, i := range p.Identities {
		if i.Kind != "task" {
			reused[i.Key] = true
		}
	}
	input, err := BuildGitHubEntityInputs(s, ids, reused)
	if err != nil || len(input.Notes) != 0 || len(input.Boards) != 0 {
		t.Fatal("unused adopted content blocked or became writes", err)
	}
}

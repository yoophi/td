package storetransfer

import (
	"strings"
	"testing"

	"github.com/marcus/td/internal/db"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
)

func sqliteReferenceFixture() *db.SQLiteWorkspaceExport {
	return &db.SQLiteWorkspaceExport{SchemaVersion: db.SchemaVersion,
		Tasks: []db.SQLiteExportTask{
			{Issue: models.Issue{ID: "parent"}},
			{Issue: models.Issue{ID: "child", ParentID: "parent"}, Dependencies: []models.IssueDependency{{IssueID: "child", DependsOnID: "parent", RelationType: "depends_on"}}, Logs: []models.Log{{ID: "log", IssueID: "child"}}},
		},
		Notes:  []models.Note{{ID: "note"}},
		Boards: []db.SQLiteExportBoard{{Board: models.Board{ID: "board", Query: `any(id, "parent", "future") AND child_of("parent")`}, Positions: []db.SQLiteExportPosition{{BoardIssue: models.BoardIssue{BoardID: "board", IssueID: "child", Position: 1}}}}},
	}
}

func TestPreviewQualifiedIdentitiesAndIntentionalQueryMiss(t *testing.T) {
	data := sqliteReferenceFixture()
	one, err := NewSQLite("one:source", data, 0)
	if err != nil {
		t.Fatal(err)
	}
	two, err := NewSQLite("other:source", data, 0)
	if err != nil {
		t.Fatal(err)
	}
	p, err := one.Preview()
	if err != nil {
		t.Fatal(err)
	}
	q, err := two.Preview()
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Identities) != 4 || len(p.QueryReferences) != 3 {
		t.Fatalf("incomplete preview: %+v", p)
	}
	for i := range p.Identities {
		if p.Identities[i].Key == q.Identities[i].Key {
			t.Fatal("different sources collided")
		}
	}
	if p.QueryReferences[1].ID != "future" || p.QueryReferences[1].Present {
		t.Fatal("query miss was removed or fabricated")
	}
	// Display data cannot bypass the immutable checksummed source.
	one.SQLite.Tasks[1].Issue.ParentID = "missing"
	if _, err := one.Preview(); err != nil {
		t.Fatalf("preview used mutable display data: %v", err)
	}
}

func TestSQLiteReferencesFailBeforeReturningSource(t *testing.T) {
	for name, mutate := range map[string]func(*db.SQLiteWorkspaceExport){
		"duplicate_task":       func(d *db.SQLiteWorkspaceExport) { d.Tasks = append(d.Tasks, d.Tasks[0]) },
		"cross_kind_collision": func(d *db.SQLiteWorkspaceExport) { d.Notes[0].ID = "parent" },
		"missing_parent":       func(d *db.SQLiteWorkspaceExport) { d.Tasks[1].Issue.ParentID = "absent" },
		"self_parent":          func(d *db.SQLiteWorkspaceExport) { d.Tasks[1].Issue.ParentID = "child" },
		"missing_dependency":   func(d *db.SQLiteWorkspaceExport) { d.Tasks[1].Dependencies[0].DependsOnID = "absent" },
		"dependency_owner":     func(d *db.SQLiteWorkspaceExport) { d.Tasks[1].Dependencies[0].IssueID = "parent" },
		"duplicate_dependency": func(d *db.SQLiteWorkspaceExport) {
			d.Tasks[1].Dependencies = append(d.Tasks[1].Dependencies, d.Tasks[1].Dependencies[0])
		},
		"history_owner":         func(d *db.SQLiteWorkspaceExport) { d.Tasks[1].Logs[0].IssueID = "parent" },
		"nil_review":            func(d *db.SQLiteWorkspaceExport) { d.Tasks[1].Reviews = []*models.IssueReview{nil} },
		"position_owner":        func(d *db.SQLiteWorkspaceExport) { d.Boards[0].Positions[0].BoardID = "another" },
		"missing_position_task": func(d *db.SQLiteWorkspaceExport) { d.Boards[0].Positions[0].IssueID = "missing" },
		"invalid_query":         func(d *db.SQLiteWorkspaceExport) { d.Boards[0].Board.Query = "status = banana" },
	} {
		t.Run(name, func(t *testing.T) {
			data := sqliteReferenceFixture()
			mutate(data)
			if source, err := NewSQLite("source", data, 0); err == nil || source != nil {
				t.Fatal("invalid source accepted")
			}
		})
	}
}

func githubReferenceFixture() *ghstore.WorkspaceExport {
	return &ghstore.WorkspaceExport{Repository: "owner/repo",
		Tasks:  []ghstore.ExportedIssue{{Version: 1, Repository: "owner/repo", Record: ghstore.Record{Issue: models.Issue{ID: "gh-1", Title: "task"}, Number: 1}, Body: "body", NativeState: "open", Author: "author"}},
		Boards: []ghstore.BoardRecord{{Board: models.Board{ID: "bd-all-issues", Name: "All Issues", IsBuiltin: true}, Number: 2, Details: ghstore.BoardDetails{Builtin: true}}},
		Native: []ghstore.NativeExport{{Number: 1, Kind: "issue", Title: "task", Body: "body", State: "open", Author: "author", Comments: []ghstore.NativeExportComment{{ID: 100}}}, {Number: 2, Kind: "board", Title: "All Issues"}},
	}
}

func TestGitHubBindingsRejectDuplicateOrForeignEvidence(t *testing.T) {
	if _, err := NewGitHub(githubReferenceFixture(), 0); err != nil {
		t.Fatalf("materialized builtin carrier rejected: %v", err)
	}
	for name, mutate := range map[string]func(*ghstore.WorkspaceExport){
		"foreign_repository": func(d *ghstore.WorkspaceExport) { d.Tasks[0].Repository = "other/repo" },
		"noncanonical_task":  func(d *ghstore.WorkspaceExport) { d.Tasks[0].Record.ID = "gh-01" },
		"unbound_task":       func(d *ghstore.WorkspaceExport) { d.Native = d.Native[1:] },
		"wrong_kind":         func(d *ghstore.WorkspaceExport) { d.Native[0].Kind = "note" },
		"duplicate_native":   func(d *ghstore.WorkspaceExport) { d.Native = append(d.Native, d.Native[0]) },
		"duplicate_comment":  func(d *ghstore.WorkspaceExport) { d.Native[1].Comments = d.Native[0].Comments },
		"conflicting_body":   func(d *ghstore.WorkspaceExport) { d.Tasks[0].Body = "changed" },
		"wrong_builtin":      func(d *ghstore.WorkspaceExport) { d.Boards[0].Details.Builtin = false },
		"missing_dependency": func(d *ghstore.WorkspaceExport) {
			d.Tasks[0].Record.Details = &ghstore.IssueDetails{Dependencies: []string{"gh-99"}}
		},
		"wrong_activity_owner": func(d *ghstore.WorkspaceExport) {
			d.Tasks[0].Activity = []models.Activity{{ID: "comment", IssueID: "gh-99"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			data := githubReferenceFixture()
			mutate(data)
			if source, err := NewGitHub(data, 0); err == nil || source != nil {
				t.Fatal("invalid binding accepted")
			}
		})
	}
}

func TestQueryReferenceTraversalLeavesTextAlone(t *testing.T) {
	d := sqliteReferenceFixture()
	d.Boards[0].Board.Query = `NOT (parent = "parent" OR any(id, "child", "future")) AND title ~ "gh-99"`
	s, err := NewSQLite("source", d, 0)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Preview()
	if err != nil {
		t.Fatal(err)
	}
	if len(p.QueryReferences) != 3 {
		t.Fatalf("missing AST references: %+v", p.QueryReferences)
	}
	for _, r := range p.QueryReferences {
		if strings.Contains(r.ID, "gh-99") {
			t.Fatal("interpreted text content as identity")
		}
	}
}

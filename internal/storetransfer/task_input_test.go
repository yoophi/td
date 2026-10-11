package storetransfer

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/db"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
)

func TestTaskInputUsesImmutableSourceStableNamespaceAndArchivesBlocks(t *testing.T) {
	data := &db.SQLiteWorkspaceExport{SchemaVersion: db.SchemaVersion, Tasks: []db.SQLiteExportTask{
		{Issue: models.Issue{ID: "td-gh-1", Title: "parent", Status: models.StatusOpen, Type: models.TypeTask, Priority: models.PriorityP2}},
		{Issue: models.Issue{ID: "td-gh-2", Title: "child", Status: models.StatusInProgress, Type: models.TypeTask, Priority: models.PriorityP1, ParentID: "td-gh-1", CreatorSession: "original"}, Dependencies: []models.IssueDependency{{IssueID: "td-gh-2", DependsOnID: "td-gh-1", RelationType: "blocks"}, {IssueID: "td-gh-2", DependsOnID: "td-gh-1", RelationType: "depends_on"}}},
	}}
	source, err := NewSQLite("namespace:한글/with spaces", data, 0)
	if err != nil {
		t.Fatal(err)
	}
	input, err := BuildTaskImportInput(source)
	if err != nil {
		t.Fatal(err)
	}
	if len(input.Items) != 2 || len(input.Keys) != 2 || len(input.Items[1].Dependencies) != 1 || input.Items[1].Dependencies[0].RelationType != "depends_on" {
		t.Fatal("lost task or activated legacy blocks")
	}
	if input.Items[0].Issue.ID == "td-gh-1" || input.Items[1].Issue.ParentID != input.Items[0].Issue.ID || input.Items[1].Dependencies[0].DependsOnID != input.Items[0].Issue.ID {
		t.Fatal("normalized SQLite IDs or their references were not adapted")
	}
	if input.Items[1].Issue.CreatorSession != "original" || len(source.SQLite.Tasks[1].Dependencies) != 2 {
		t.Fatal("source provenance modified")
	}
	data.Tasks[0].Issue.Title = "changed"
	changed, err := NewSQLite("namespace:한글/with spaces", data, 0)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildTaskImportInput(changed)
	if err != nil {
		t.Fatal(err)
	}
	if input.SourceSHA256 == second.SourceSHA256 || input.Items[0].SourceNamespace != second.Items[0].SourceNamespace {
		t.Fatal("import identity depends on changing payload")
	}
	source.SQLite.Tasks = nil
	immutable, err := BuildTaskImportInput(source)
	if err != nil || len(immutable.Items) != 2 {
		t.Fatal("mutable display source used")
	}
	if _, err := BuildTaskImportInput(nil); err == nil {
		t.Fatal("nil source accepted")
	}
}

func githubTaskInputFixture(t *testing.T) *Source {
	t.Helper()
	now := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	data := &ghstore.WorkspaceExport{Repository: "owner/repo"}
	for n := 1; n <= 2; n++ {
		issue := models.Issue{ID: []string{"gh-1", "gh-2"}[n-1], Title: "task", Description: "body", Status: models.StatusOpen, Type: models.TypeTask, Priority: models.PriorityP2, CreatedAt: now, UpdatedAt: now}
		details := &ghstore.IssueDetails{Status: models.StatusOpen}
		activity := []models.Activity{}
		if n == 1 {
			issue.Status = models.StatusInProgress
			issue.ParentID = "gh-2"
			issue.CreatorSession = "source-creator"
			issue.ImplementerSession = "source-worker"
			issue.ReviewerSession = "source-reviewer"
			issue.ReviewRequestedBySession = "source-requester"
			issue.ClosedBySession = "source-closer"
			issue.ReviewedAt = &now
			issue.DeletedAt = &now
			issue.DueDate = new("2026-10-13")
			details = &ghstore.IssueDetails{Status: issue.Status, ParentID: issue.ParentID, CreatorSession: issue.CreatorSession, ImplementerSession: issue.ImplementerSession, ReviewerSession: issue.ReviewerSession, ReviewRequestedBySession: issue.ReviewRequestedBySession, ClosedBySession: issue.ClosedBySession, ReviewedAt: &now, DeletedAt: &now, DueDate: issue.DueDate, Dependencies: []string{"gh-2"}, Files: []models.IssueFile{{FilePath: "src.go", Role: models.FileRoleImplementation, LinkedAt: now}, {IssueID: "gh-1", FilePath: "test.go", Role: models.FileRoleTest, LinkedAt: now}}}
			activity = []models.Activity{{ID: "ghc-1", IssueID: "gh-1", Kind: "log", SessionID: "source-worker", WorkSessionID: "ws-source", Message: "original progress", LogType: models.LogTypeProgress, CreatedAt: now}, {ID: "ghc-2", IssueID: "gh-1", Kind: "handoff", SessionID: "source-worker", Done: []string{"original done"}, CreatedAt: now}}
		}
		meta, err := json.Marshal(map[string]any{"type": "task", "priority": "P2", "details": details})
		if err != nil {
			t.Fatal(err)
		}
		body := "body\n\n<!-- td:issue:v1\n" + string(meta) + "\n-->"
		data.Tasks = append(data.Tasks, ghstore.ExportedIssue{Version: 1, Repository: data.Repository, Record: ghstore.Record{Issue: issue, Number: n, Details: details}, Body: body, NativeState: "open", Author: "original-author", Activity: activity})
		data.Native = append(data.Native, ghstore.NativeExport{Number: n, Kind: "issue", Title: issue.Title, Body: body, State: "open", Author: "original-author", CreatedAt: now, UpdatedAt: now})
	}
	source, err := NewGitHub(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func TestSQLiteTaskProjectionMapsRelationsAndKeepsHistoryPassive(t *testing.T) {
	source := githubTaskInputFixture(t)
	input, err := BuildTaskImportInput(source)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for key, importKey := range input.Keys {
		if strings.HasSuffix(importKey, ":gh-1") {
			ids[key] = "td-gh-1"
		} else {
			ids[key] = "td-two"
		}
	}
	rows, err := ProjectSQLiteTasks(source, ids)
	if err != nil {
		t.Fatal(err)
	}
	r := rows[0]
	if r.Issue.ID != "td-gh-1" || r.Issue.ParentID != "td-two" || r.Dependencies[0].DependsOnID != "td-two" || r.Files[0].IssueID != "td-gh-1" || r.Files[0].ID == "" || r.Files[0].ID == r.Files[1].ID {
		t.Fatal("source IDs leaked into destination")
	}
	if r.Issue.CreatorSession != "" || r.Issue.ImplementerSession != "" || r.Issue.ReviewerSession != "" || r.Issue.ReviewRequestedBySession != "" || r.Issue.ClosedBySession != "" || r.Issue.ReviewedAt != nil || len(r.Reviews) != 0 || len(r.SessionHistory) != 0 {
		t.Fatal("source actors or approvals activated")
	}
	if r.Issue.Status != models.StatusInProgress || r.Issue.DeletedAt == nil || r.Issue.DueDate == nil {
		t.Fatal("portable state or dates lost")
	}
	if !r.Logs[0].Historical || r.Logs[0].OriginalID != "ghc-1" || r.Logs[0].OriginalSessionID != "source-worker" || !strings.HasPrefix(r.Logs[0].SessionID, "imported-") || r.Logs[0].WorkSessionID != "" || !r.Handoffs[0].Historical {
		t.Fatal("history provenance or passive session boundary lost")
	}
	if !strings.Contains(input.Items[0].GitHub.Body, "source-creator") || source.GitHub.Tasks[0].Record.CreatorSession != "source-creator" {
		t.Fatal("original provenance changed")
	}
	for _, bad := range []map[string]string{{}, {"unknown": "td-one"}} {
		if rows, err := ProjectSQLiteTasks(source, bad); err == nil || rows != nil {
			t.Fatal("invalid map returned partial rows")
		}
	}
	for key := range ids {
		ids[key] = "td-same"
	}
	if rows, err := ProjectSQLiteTasks(source, ids); err == nil || rows != nil {
		t.Fatal("duplicate mapping accepted")
	}
}

func TestGitHubFileProjectionRejectsConflictingOwnershipAndPaths(t *testing.T) {
	for _, change := range []func(*ghstore.WorkspaceExport){
		func(data *ghstore.WorkspaceExport) { data.Tasks[0].Record.Details.Files[0].IssueID = "gh-2" },
		func(data *ghstore.WorkspaceExport) {
			data.Tasks[0].Record.Details.Files[1].FilePath = data.Tasks[0].Record.Details.Files[0].FilePath
		},
	} {
		s := githubTaskInputFixture(t)
		change(s.GitHub)
		if invalid, err := NewGitHub(s.GitHub, 0); err == nil || invalid != nil {
			t.Fatal("conflicting file owner/path accepted")
		}
	}
}

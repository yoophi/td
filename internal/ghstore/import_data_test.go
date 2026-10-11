package ghstore

import (
	"encoding/json"
	"github.com/marcus/td/internal/models"
	"strings"
	"testing"
)

func importTestItem(id string) ImportItem {
	return ImportItem{Issue: models.Issue{ID: id, Title: "Imported task", Type: models.TypeTask, Priority: models.PriorityP2, Status: models.StatusOpen}}
}
func TestImportSchemaLegacyAndStableIdentity(t *testing.T) {
	data := []byte(`[{"issue":{"id":"td-abc","title":"Legacy","type":"task","priority":"P2","status":"open"},"handoff":{"id":"ho-1","done":["done"]},"dependencies":["td-def"]},{"issue":{"id":"td-def","title":"Parent","type":"task","priority":"P2","status":"closed"}}]`)
	rows, err := ParseImportJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows[0].Handoffs) != 1 || len(rows[0].Dependencies) != 1 || rows[0].SourceKey() != "legacy:td-abc" {
		t.Fatal(rows)
	}
	a, err := rows[0].Digest()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	again, err := ParseImportJSON(encoded)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := again[0].Digest()
	if a != b {
		t.Fatal("unstable import identity")
	}
}
func TestImportSchemaRejectsContaminatedDataBeforeWrites(t *testing.T) {
	for _, raw := range []string{
		`null`, `[] {}`, `[{"future":1}]`,
		`[{"issue":{"id":"td-abc","title":"Task","type":"task","priority":"P2","status":"open","future":1}}]`,
		`[{"issue":{"id":"gh-01","title":"Task","type":"task","priority":"P2","status":"open"}}]`,
	} {
		if _, err := ParseImportJSON([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	cases := []struct {
		name   string
		mutate func(*ImportItem)
	}{
		{"missing parent", func(e *ImportItem) { e.Issue.ParentID = "td-missing" }},
		{"self parent", func(e *ImportItem) { e.Issue.ParentID = e.Issue.ID }},
		{"status", func(e *ImportItem) { e.Issue.Status = "invalid" }},
		{"date", func(e *ImportItem) { e.Issue.DueDate = new("2026-02-30") }},
		{"log", func(e *ImportItem) { e.Logs = []models.Log{{ID: "log-1", Message: "valid", Type: "invalid"}} }},
		{"file", func(e *ImportItem) {
			e.Files = []models.IssueFile{{FilePath: "../secret", Role: models.FileRoleReference}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := importTestItem("td-abc")
			tc.mutate(&e)
			if err := ValidateImportItems([]ImportItem{e}); err == nil {
				t.Fatal("accepted invalid import")
			}
		})
	}
	a, b := importTestItem("td-abc"), importTestItem("td-def")
	a.Issue.ParentID = b.Issue.ID
	b.Issue.ParentID = a.Issue.ID
	if err := ValidateImportItems([]ImportItem{a, b}); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatal(err)
	}
}

func TestImportNormalizedSQLiteIDsAndNativeBoundary(t *testing.T) {
	parent, child := importTestItem("td-gh-1"), importTestItem("td-gh-2")
	child.Issue.ParentID = parent.Issue.ID
	child.Dependencies = []models.IssueDependency{{IssueID: child.Issue.ID, DependsOnID: parent.Issue.ID, RelationType: "depends_on"}}
	data, err := json.Marshal([]ImportItem{parent, child})
	if err != nil {
		t.Fatal(err)
	}
	items, err := ParseImportJSON(data)
	if err != nil || items[1].Issue.ParentID != "td-gh-1" {
		t.Fatal("normalized legacy references rejected", err)
	}
	for _, id := range []string{"td-gh-0", "td-gh-01", "td-gh--1", "td-gh-abc", "gh-01"} {
		bad := importTestItem(id)
		if err := ValidateImportItems([]ImportItem{bad}); err == nil {
			t.Fatal("malformed normalized/native ID accepted", id)
		}
	}
	parent.GitHub = &ExportedIssue{Version: 1, Repository: "owner/repo", Record: Record{Issue: parent.Issue, Number: 1}, NativeState: "open"}
	if err := ValidateImportItems([]ImportItem{parent}); err == nil {
		t.Fatal("normalized SQLite ID became native GH identity")
	}
	if err := ValidateImportItems([]ImportItem{items[0], items[0]}); err == nil {
		t.Fatal("duplicate normalized source identity accepted")
	}
	a, b := items[0], items[0]
	a.SourceNamespace = "project-a"
	b.SourceNamespace = "project-b"
	if err := ValidateImportItems([]ImportItem{a, b}); err != nil {
		t.Fatal("distinct legacy namespaces collided", err)
	}
	b.SourceNamespace = a.SourceNamespace
	if err := ValidateImportItems([]ImportItem{a, b}); err == nil {
		t.Fatal("same namespace duplicate accepted")
	}
	items[1].Issue.ParentID = "td-gh-404"
	if err := ValidateImportItems(items); err == nil {
		t.Fatal("dangling normalized reference accepted")
	}
}
func TestImportGitHubSourceEnvelopeAndEntityValidation(t *testing.T) {
	e := importTestItem("gh-1")
	e.Issue.Description = "native"
	e.GitHub = &ExportedIssue{Version: 1, Repository: "owner/repo", Body: "native", NativeState: "open", Record: Record{Issue: e.Issue, Number: 1}}
	if err := ValidateImportItems([]ImportItem{e}); err != nil {
		t.Fatal(err)
	}
	other := e
	copy := *e.GitHub
	other.GitHub = &copy
	other.GitHub.Repository = "other/repo"
	if err := ValidateImportItems([]ImportItem{e, other}); err != nil {
		t.Fatal("repository-local IDs collided", err)
	}
	e.GitHub.Version = 2
	if err := ValidateImportItems([]ImportItem{e}); err == nil {
		t.Fatal("future version accepted")
	}
	e.GitHub.Version = 1
	e.GitHub.Record.Title = "different"
	if err := ValidateImportItems([]ImportItem{e}); err == nil {
		t.Fatal("inconsistent archive accepted")
	}
}

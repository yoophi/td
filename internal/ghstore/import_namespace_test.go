package ghstore

import (
	"encoding/json"
	"testing"

	"github.com/marcus/td/internal/models"
)

func TestImportNamespacesSeparateLegacyProjectsAndRetainReferences(t *testing.T) {
	a, b := importTestItem("td-parent"), importTestItem("td-child")
	b.Issue.ParentID = a.Issue.ID
	b.Dependencies = []models.IssueDependency{{DependsOnID: a.Issue.ID, RelationType: "depends_on"}}
	items := []ImportItem{a, b}
	if err := SetImportSourceNamespace(items, "project-a"); err != nil {
		t.Fatal(err)
	}
	if items[1].ReferenceKey(items[1].Issue.ParentID) != items[0].SourceKey() {
		t.Fatal("parent was detached from its source namespace")
	}
	other := []ImportItem{a, b}
	if err := SetImportSourceNamespace(other, "project-b"); err != nil {
		t.Fatal(err)
	}
	if importOperationID(items[0].SourceKey()) == importOperationID(other[0].SourceKey()) {
		t.Fatal("independent legacy projects share a creation identity")
	}
	combined := append(items, other...)
	data, err := json.Marshal(combined)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseImportJSON(data)
	if err != nil || len(parsed) != 4 || parsed[0].SourceNamespace != "project-a" || parsed[2].SourceNamespace != "project-b" {
		t.Fatalf("namespaces did not survive portable JSON: %+v %v", parsed, err)
	}
	// A parent available only in another project must not resolve by td ID.
	combined = []ImportItem{other[0], items[1]}
	if err := ValidateImportItems(combined); err == nil {
		t.Fatal("cross-project relationship accepted")
	}
}

func TestImportSourceNamespaceRejectsAmbiguityAndInvalidDeclarations(t *testing.T) {
	item := importTestItem("td-one")
	item.SourceNamespace = "declared"
	items := []ImportItem{item}
	if err := SetImportSourceNamespace(items, "different"); err == nil || items[0].SourceNamespace != "declared" {
		t.Fatal("declared source overwritten", err)
	}
	for _, name := range []string{"has:separator", " has-space", "../project", "https://host/repo"} {
		if err := SetImportSourceNamespace([]ImportItem{importTestItem("td-one")}, name); err == nil {
			t.Errorf("unsafe namespace accepted: %q", name)
		}
	}
	item.GitHub = &ExportedIssue{Repository: "owner/repo"}
	if err := ValidateImportItems([]ImportItem{item}); err == nil {
		t.Fatal("GitHub source namespace override accepted")
	}
	item.SourceNamespace = ""
	if err := SetImportSourceNamespace([]ImportItem{item}, "legacy-project"); err == nil {
		t.Fatal("legacy --source silently accepted for GitHub-only input")
	}
}

package config

import (
	"strings"
	"testing"

	"github.com/marcus/td/internal/models"
)

func TestBackendStoreParserAndAssociationValidation(t *testing.T) {
	for _, tc := range []struct{ input, kind, url string }{{"sqlite", StoreSQLite, ""}, {"gh-issue", StoreGitHub, ""}, {"td-backend", StoreBackend, ""}, {"td-backend,http://localhost:7000/", StoreBackend, "http://localhost:7000"}, {"td-backend,https://example.test/api,a", StoreBackend, "https://example.test/api,a"}} {
		kind, url, e := ParseStore(tc.input)
		if e != nil || kind != tc.kind || url != tc.url {
			t.Fatal(tc, kind, url, e)
		}
	}
	for _, input := range []string{"unknown", "sqlite,http://localhost:7000", "gh-issue,origin", "td-backend,", "td-backend,ftp://host", "td-backend,http://user:private-password@host", "td-backend,http://host?token=private-password", "td-backend,http://host/#fragment", "td-backend,http:///path"} {
		_, _, e := ParseStore(input)
		if e == nil || strings.Contains(e.Error(), "private-password") {
			t.Fatal(input, e)
		}
	}
	dir := t.TempDir()
	b := models.BackendStoreConfig{URL: "http://localhost:7000/", ProjectID: "prj_0199dc38-1200-7000-8000-000000000001"}
	if e := Save(dir, &models.Config{Store: StoreSQLite, FocusedIssueID: "old", PaneHeights: [3]float64{.1, .2, .7}}); e != nil {
		t.Fatal(e)
	}
	if e := SetBackend(dir, b, StoreSQLite); e != nil {
		t.Fatal(e)
	}
	cfg, e := Load(dir)
	if e != nil || cfg.Backend.URL != "http://localhost:7000" || cfg.FocusedIssueID != "old" || cfg.PaneHeights[2] != .7 {
		t.Fatal(cfg, e)
	}
	b.ProjectID = "prj_0199dc38-1200-7000-8000-000000000002"
	if e = SetBackend(dir, b, StoreBackend); e == nil {
		t.Fatal("conflicting association overwritten")
	}
	if e = SetStore(dir, StoreSQLite, nil); e != nil {
		t.Fatal(e)
	}
	cfg, e = Load(dir)
	if e != nil || cfg.Backend != nil {
		t.Fatal("stale backend retained", cfg, e)
	}
}

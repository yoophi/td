package cmd

import (
	"encoding/json"
	"github.com/marcus/td/internal/db"
	"github.com/marcus/td/internal/ghstore"
	"strings"
	"testing"
)

func TestLegacyImportEnvelopeKeepsLenientReadingAndGitHubIsStrict(t *testing.T) {
	data := []byte(`[{"issue":{"id":"td-abc","title":"Task","future":1},"future_envelope":true}]`)
	var local []exportedItem
	if err := json.Unmarshal(data, &local); err != nil || len(local) != 1 {
		t.Fatal("legacy SQLite envelope behavior changed", err)
	}
	if _, err := ghstore.ParseImportJSON(data); err == nil {
		t.Fatal("GitHub import silently discarded unknown fields")
	}
}

func TestSQLiteImportRejectsExplicitGitHubAdoptionBeforeAnyTaskWrite(t *testing.T) {
	database, err := db.Initialize(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	data := []byte(`[{"issue":{"id":"td-first","title":"Must not be inserted"}},{"issue":{"id":"td-second","title":"Passive source"},"adopt_target_id":"gh-1"}]`)
	for _, dryRun := range []bool{false, true} {
		count, err := importJSON(database, data, dryRun, true)
		if err == nil || count != 0 || !strings.Contains(err.Error(), "adopt_target_id") {
			t.Fatal("SQLite silently ignored adoption decision", count, err)
		}
		if issue, err := database.GetIssue("td-first"); err == nil || issue != nil {
			t.Fatal("earlier input row was written before rejection", issue, err)
		}
	}
}

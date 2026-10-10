package testutil

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestGitHubJournalFixtureReadsFullUpdateIntentReceipts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "native.json")
	state := map[string]any{"comments": []map[string]string{{"body": "td action fixture: update gh-1 (applied)\n\n<!-- td:action:v8\n{\"action\":\"update\",\"phase\":\"applied\",\"session_id\":\"fixture-session\",\"carrier_id\":\"gh-999\",\"update_intent\":{\"target\":{}}}\n-->"}}}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	rows := (GitHubJournalFixture{StatePath: path}).Journals(t)
	if len(rows) != 1 || rows[0].Action != "update" || rows[0].Phase != "applied" || rows[0].SessionID != "fixture-session" {
		t.Fatal("native v8 update receipt omitted by fixture observer", rows)
	}
}

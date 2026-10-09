package cmd

import (
	"encoding/json"
	"github.com/marcus/td/internal/ghstore"
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

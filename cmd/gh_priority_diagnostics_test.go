package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/marcus/td/internal/ghstore"
)

func TestGitHubListPriorityMismatchHumanAndJSON(t *testing.T) {
	githubScheduleTestDir(t)
	out, err := executeGitHubTest(githubTestCommand(listCmd), "--id", "gh-1")
	if err != nil || !strings.Contains(out, "priority labels differ from authoritative metadata P2") {
		t.Fatalf("missing human warning: %s %v", out, err)
	}
	out, err = executeGitHubTest(githubTestCommand(listCmd), "--id", "gh-1", "--json")
	var rows []ghstore.Record
	if err != nil || json.Unmarshal([]byte(out), &rows) != nil || len(rows) != 1 || rows[0].PriorityLabelDiagnostic == "" {
		t.Fatalf("invalid diagnostic JSON: %s %v", out, err)
	}
}

package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marcus/td/internal/db"
)

func TestGitHubSearchCLIFormattingFiltersAndValidation(t *testing.T) {
	dir := githubScheduleTestDir(t)
	out, stderr, err := executeGitHubReadTest(searchCmd, "Schedule", "--json", "--limit", "1")
	var rows []db.SearchResult
	if err != nil || json.Unmarshal([]byte(out), &rows) != nil || len(rows) != 1 || rows[0].Issue.ID != "gh-2" || rows[0].Score != 70 || stderr != "" {
		t.Fatal(out, stderr, err)
	}
	out, stderr, err = executeGitHubReadTest(searchCmd, "Schedule", "--status", "closed", "--json")
	if err != nil || json.Unmarshal([]byte(out), &rows) != nil || len(rows) != 1 || rows[0].Issue.ID != "gh-3" || stderr != "" {
		t.Fatal(out, stderr, err)
	}
	out, stderr, err = executeGitHubReadTest(searchCmd, "Schedule", "--priority", "P0", "--show-score")
	if err != nil || !strings.Contains(out, "gh-2") || !strings.Contains(out, "[score:70 title]") || strings.Contains(out, "gh-1") || stderr != "" {
		t.Fatal(out, stderr, err)
	}
	out, stderr, err = executeGitHubReadTest(searchCmd, "not-present", "--json")
	if err != nil || strings.TrimSpace(out) != "[]" || stderr != "" {
		t.Fatal(out, stderr, err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("search created SQLite")
	}
	t.Setenv("PATH", t.TempDir())
	for _, args := range [][]string{{"text", "--status", "invalid"}, {"text", "--type", "invalid"}, {"text", "--priority", "invalid"}, {"text", "--limit", "-1"}} {
		out, _, err := executeGitHubReadTest(searchCmd, args...)
		if err == nil || out != "" || strings.Contains(err.Error(), "gh CLI") {
			t.Fatal("invalid filter reached network", args, out, err)
		}
	}
}

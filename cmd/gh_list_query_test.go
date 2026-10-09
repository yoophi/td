package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marcus/td/internal/models"
)

func TestGitHubListTDQFlagsAndPositionalQueries(t *testing.T) {
	dir := githubScheduleTestDir(t)
	for _, args := range [][]string{{"due < 2021-01-01", "--json"}, {"--filter", "due < 2021-01-01", "--format", "json"}} {
		out, stderr, err := executeGitHubReadTest(listCmd, args...)
		var rows []models.Issue
		if err != nil || json.Unmarshal([]byte(out), &rows) != nil || len(rows) != 1 || rows[0].ID != "gh-3" || stderr != "" {
			t.Fatal(args, out, stderr, err)
		}
	}
	out, stderr, err := executeGitHubReadTest(listCmd, "type = task", "--limit", "1", "--json")
	var rows []models.Issue
	if err != nil || json.Unmarshal([]byte(out), &rows) != nil || len(rows) != 1 || rows[0].ID != "gh-2" || !strings.Contains(stderr, "showing 1 of 3") {
		t.Fatal(out, stderr, err)
	}
	out, _, err = executeGitHubReadTest(listCmd, "id = gh-1", "--long")
	if err != nil || !strings.Contains(out, "Schedule fixture 1") {
		t.Fatal(out, err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("list query created SQLite")
	}
	t.Setenv("PATH", t.TempDir())
	for _, args := range [][]string{{"--filter", ""}, {"type = task", "--filter", "type = task"}, {"unknown_field = x"}, {"type = task", "--sort", "invalid"}, {"type = task", "--limit", "-1"}} {
		out, _, err := executeGitHubReadTest(listCmd, args...)
		if err == nil || out != "" || strings.Contains(err.Error(), "gh CLI") {
			t.Fatal("invalid TDQ reached network", args, out, err)
		}
	}
}

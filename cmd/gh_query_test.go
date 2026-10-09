package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func executeGitHubReadTest(original *cobra.Command, args ...string) (string, string, error) {
	cmd := githubTestCommand(original)
	var out, diagnostics bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&diagnostics)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return out.String(), diagnostics.String(), err
}

func TestGitHubQueryCLIFormatsLimitsDatesAndFailures(t *testing.T) {
	dir := githubScheduleTestDir(t)
	for _, tc := range []struct {
		args []string
		want string
	}{{[]string{"type = task", "--output", "count"}, "3\n"}, {[]string{"id = gh-1", "--output", "ids"}, "gh-1\n"}, {[]string{"id = missing", "--output", "ids"}, ""}, {[]string{"due < 2021-01-01", "--output", "ids"}, "gh-3\n"}} {
		out, stderr, err := executeGitHubReadTest(queryCmd, tc.args...)
		if err != nil || out != tc.want || stderr != "" {
			t.Fatal(tc, out, stderr, err)
		}
	}
	out, stderr, err := executeGitHubReadTest(queryCmd, "type = task", "--limit", "1", "--json")
	var rows []map[string]any
	if err != nil || json.Unmarshal([]byte(out), &rows) != nil || len(rows) != 1 || !strings.Contains(stderr, "showing 1 of 3") {
		t.Fatal(out, stderr, err)
	}
	out, stderr, err = executeGitHubReadTest(queryCmd, "type = task", "--max-scan", "1", "--output", "count")
	if err != nil || out != "1\n" || !strings.Contains(stderr, "first 1 issues") {
		t.Fatal(out, stderr, err)
	}
	out, stderr, err = executeGitHubReadTest(queryCmd, "id = missing", "--json")
	if err != nil || strings.TrimSpace(out) != "[]" || stderr != "" {
		t.Fatal(out, stderr, err)
	}
	if _, _, err := executeGitHubReadTest(queryCmd, "comment.text ~ missing", "--json"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("SQLite created")
	}
	t.Setenv("PATH", t.TempDir())
	for _, args := range [][]string{{"unknown_field = x"}, {"type = task", "--output", "xml"}, {"type = task", "--limit", "-1"}} {
		out, _, err := executeGitHubReadTest(queryCmd, args...)
		if err == nil || out != "" || strings.Contains(err.Error(), "gh CLI") {
			t.Fatal("invalid query reached network", args, out, err)
		}
	}
	for _, args := range [][]string{{"--fields"}, {"--examples"}, {"due <= today", "--explain"}} {
		if _, _, err := executeGitHubReadTest(queryCmd, args...); err != nil {
			t.Fatal("offline helper required gh/git", args, err)
		}
	}
}

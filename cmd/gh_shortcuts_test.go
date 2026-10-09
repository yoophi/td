package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/models"
	"github.com/spf13/cobra"
)

func shortcutTestCommand(original *cobra.Command, create bool) *cobra.Command {
	cmd := &cobra.Command{Use: original.Use, Args: original.Args, RunE: original.RunE, SilenceUsage: true, SilenceErrors: true}
	if create {
		registerCreateFlags(cmd)
	} else {
		registerListFlags(cmd)
	}
	cmd.Flags().Bool("json", false, "")
	return cmd
}

func TestGitHubTypeShortcuts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake gh uses POSIX shell")
	}
	dir := storeTestDir(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("TD_CONTEXT_ID", "synthetic-type-list-test")
	runGit(t, dir, "init")
	runGit(t, dir, "remote", "add", "origin", "https://github.com/owner/repo.git")
	if err := config.SetStore(dir, "gh-issue", &models.GitHubStoreConfig{Repo: "owner/repo", Remote: "origin"}); err != nil {
		t.Fatal(err)
	}
	data := []map[string]any{}
	for i, typ := range []string{"task", "epic", "bug", "task"} {
		state := "open"
		if i == 3 {
			state = "closed"
		}
		body := "Description\n\n<!-- td:issue:v1\n{\"type\":\"" + typ + "\",\"priority\":\"P2\",\"points\":0}\n-->"
		data = append(data, map[string]any{"number": i + 1, "title": typ + " title", "state": state, "body": body})
	}
	fixture, err := json.Marshal([]any{data})
	if err != nil {
		t.Fatal(err)
	}
	fixturePath := filepath.Join(t.TempDir(), "issues.json")
	if err := os.WriteFile(fixturePath, fixture, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TD_TEST_ISSUES", fixturePath)
	openFixture, err := json.Marshal([]any{data[:3]})
	if err != nil {
		t.Fatal(err)
	}
	openPath := filepath.Join(t.TempDir(), "open.json")
	if err := os.WriteFile(openPath, openFixture, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TD_TEST_OPEN_ISSUES", openPath)
	bin := t.TempDir()
	script := `#!/bin/sh
if [ "$1" = auth ]; then exit 0; fi
for arg in "$@"; do
 if [ "$arg" = --include ]; then printf 'HTTP/2.0 200 OK\nContent-Type: application/json\r\n\r\n'; break; fi
done
if [ "$4" = repos/owner/repo ]; then
 printf '%s' '{"full_name":"owner/repo","has_issues":true}'
elif [ "$6" = "repos/owner/repo/issues?state=open&per_page=100" ]; then
 cat "$TD_TEST_OPEN_ISSUES"
elif [ "$5" = GET ]; then
 cat "$TD_TEST_ISSUES"
else
 echo 'unexpected mutation' >&2
 exit 1
fi
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, tc := range []struct {
		cmd  *cobra.Command
		args []string
		ids  []string
	}{
		{taskListCmd, nil, []string{"gh-1"}},
		{taskListCmd, []string{"--all"}, []string{"gh-1", "gh-4"}},
		{epicListCmd, []string{"--all"}, []string{"gh-2"}},
		{taskListCmd, []string{"--all", "--limit", "1", "--reverse", "--sort", "id"}, []string{"gh-4"}},
		{taskListCmd, []string{"--type", "bug"}, []string{"gh-1"}},
	} {
		cmd := shortcutTestCommand(tc.cmd, false)
		out, err := executeGitHubTest(cmd, append(tc.args, "--json")...)
		var issues []models.Issue
		if err != nil || json.Unmarshal([]byte(out), &issues) != nil {
			t.Fatalf("%v: %s %v", tc.args, out, err)
		}
		if len(issues) != len(tc.ids) {
			t.Fatalf("%v: %s", tc.args, out)
		}
		for i, id := range tc.ids {
			if issues[i].ID != id {
				t.Fatalf("want %s, got %s", id, issues[i].ID)
			}
		}
	}
	for _, original := range []*cobra.Command{taskListCmd, epicListCmd} {
		out, err := executeGitHubTest(shortcutTestCommand(original, false), "--mine", "--json")
		if err != nil || strings.TrimSpace(out) != "[]" {
			t.Fatalf("unowned fixture should not match mine: %s %v", out, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("created SQLite DB")
	}
}

func TestTypeCreateShortcutRichTextSQLite(t *testing.T) {
	database, _ := setupReadJSONTest(t)
	for _, tc := range []struct {
		cmd *cobra.Command
		typ models.Type
	}{{taskCreateCmd, models.TypeTask}, {epicCreateCmd, models.TypeEpic}} {
		cmd := shortcutTestCommand(tc.cmd, true)
		var runErr error
		out := captureStdout(t, func() {
			_, runErr = executeGitHubTest(cmd, "Shortcut rich text issue", "--body", "Body text", "--acceptance", "Acceptance text", "--points", "3", "--json")
		})
		if runErr != nil {
			t.Fatal(runErr)
		}
		var issue models.Issue
		if err := json.Unmarshal([]byte(out), &issue); err != nil {
			t.Fatalf("%s: %v", out, err)
		}
		stored, err := database.GetIssue(issue.ID)
		if err != nil {
			t.Fatal(err)
		}
		issue = *stored
		if issue.Type != tc.typ || issue.Description != "Body text" || issue.Acceptance != "Acceptance text" || issue.Points != 3 {
			t.Fatalf("lost fields: %+v", issue)
		}
	}
}

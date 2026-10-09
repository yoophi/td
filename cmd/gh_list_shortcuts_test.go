package cmd

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marcus/td/internal/models"
	"github.com/spf13/cobra"
)

func TestGitHubReadShortcutsDependencyAndEmptyJSON(t *testing.T) {
	dir := githubScheduleTestDir(t)
	if _, _, err := executeGitHubReadTest(createCmd, "Synthetic readiness fixture", "--json"); err != nil {
		t.Fatal(err)
	}
	path := os.Getenv("TD_SCHEDULE_STATE")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	issues := state["issues"].(map[string]any)
	set := func(id, status string, details map[string]any) {
		i := issues[id].(map[string]any)
		i["body"] = "<!-- td:issue:v1\n" + mustJSON(map[string]any{"type": "task", "priority": "P2", "points": 0, "details": details}) + "\n-->"
		i["labels"] = []map[string]string{{"name": "td:" + status}}
		i["state"] = "open"
	}
	set("1", "blocked", map[string]any{"status": "blocked"})
	set("2", "open", map[string]any{"status": "open", "dependencies": []string{"gh-1"}})
	write := func() {
		data, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	for _, tc := range []struct {
		cmd  *cobra.Command
		want string
	}{{blockedListCmd, "gh-1"}, {readyCmd, "gh-6"}} {
		out, stderr, err := executeGitHubReadTest(tc.cmd, "--json")
		var rows []models.Issue
		if err != nil || json.Unmarshal([]byte(out), &rows) != nil || len(rows) != 1 || rows[0].ID != tc.want || stderr != "" {
			t.Fatal(tc.want, out, stderr, err)
		}
	}
	out, _, err := executeGitHubReadTest(nextCmd, "--json")
	var one models.Issue
	if err != nil || json.Unmarshal([]byte(out), &one) != nil || one.ID != "gh-6" {
		t.Fatal(out, err)
	}
	for _, command := range []*cobra.Command{inReviewCmd, reviewableCmd} {
		out, _, err := executeGitHubReadTest(command, "--json")
		if err != nil || !json.Valid([]byte(out)) {
			t.Fatal(out, err)
		}
		if command == inReviewCmd && out != "[]\n" {
			t.Fatal(out)
		}
		if command == reviewableCmd && out != "{\"awaiting\":[],\"ready_to_close\":[]}\n" {
			t.Fatal(out)
		}
	}
	set("6", "open", map[string]any{"status": "open", "deleted_at": "2026-01-01T00:00:00Z"})
	write()
	out, _, err = executeGitHubReadTest(nextCmd, "--json")
	if err != nil || out != "null\n" {
		t.Fatal(out, err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("SQLite created")
	}
}

func TestGitHubShortcutHumanOutputUsesStdout(t *testing.T) {
	githubScheduleTestDir(t)
	command := githubTestCommand(inReviewCmd)
	command.SetErr(io.Discard)
	var runErr error
	out := captureStdout(t, func() { runErr = command.ExecuteContext(context.Background()) })
	if runErr != nil || !strings.Contains(out, "No issues in review") {
		t.Fatal(out, runErr)
	}
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

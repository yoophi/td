package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marcus/td/internal/models"
	"github.com/spf13/cobra"
)

func TestGitHubCRUDRichInputsClearAppendAndShortcuts(t *testing.T) {
	dir := githubScheduleTestDir(t)
	description, acceptance := filepath.Join(dir, "description.md"), filepath.Join(dir, "acceptance.md")
	if err := os.WriteFile(description, []byte("# Description\n\n한글 details\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(acceptance, []byte("- Acceptance one\n- Acceptance two\n"), 0600); err != nil {
		t.Fatal(err)
	}
	execute := func(original *cobra.Command, args ...string) string {
		t.Helper()
		out, err := executeGitHubTest(githubTestCommand(original), append(args, "--json")...)
		if err != nil || !json.Valid([]byte(out)) {
			t.Fatal(out, err)
		}
		return out
	}
	read := func(id string) models.Issue {
		t.Helper()
		var r models.Issue
		if err := json.Unmarshal([]byte(execute(showCmd, id)), &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	execute(taskCreateCmd, "CRUD task shortcut fixture", "--description-file", description, "--acceptance-file", acceptance, "--minor", "--labels", "alpha,beta", "--points", "5")
	r := read("6")
	if r.Type != models.TypeTask || !r.Minor || r.Points != 5 || len(r.Labels) != 3 || !strings.Contains(r.Description, "한글 details\n") || !strings.HasSuffix(r.Acceptance, "Acceptance two\n") {
		t.Fatal(r)
	}
	execute(epicCreateCmd, "CRUD epic shortcut fixture")
	if read("7").Type != models.TypeEpic {
		t.Fatal("epic shortcut")
	}
	stdinCmd := githubTestCommand(createCmd)
	stdinCmd.SetIn(strings.NewReader("stdin description\nline two\n"))
	out, err := executeGitHubTest(stdinCmd, "CRUD stdin fixture", "--description-file", "-", "--json")
	if err != nil || !strings.Contains(out, "stdin description") {
		t.Fatal(out, err)
	}
	execute(updateCmd, "6", "--description", "appended description", "--acceptance", "appended acceptance", "--append", "--sprint", "iteration-one")
	r = read("6")
	if !strings.HasSuffix(r.Description, "\n\nappended description") || !strings.HasSuffix(r.Acceptance, "\n\nappended acceptance") || r.Sprint != "iteration-one" {
		t.Fatal(r)
	}
	execute(updateCmd, "6", "--description", "", "--acceptance", "", "--sprint", "", "--labels", "")
	r = read("6")
	if r.Description != "" || r.Acceptance != "" || r.Sprint != "" || len(r.Labels) != 1 || r.Labels[0] != "td:open" {
		t.Fatal("empty clear lost", r)
	}
	stdinCmd = githubTestCommand(updateCmd)
	stdinCmd.SetIn(strings.NewReader("stdin acceptance\n"))
	if out, err := executeGitHubTest(stdinCmd, "6", "--acceptance-file", "-", "--json"); err != nil || !json.Valid([]byte(out)) {
		t.Fatal(out, err)
	}
	for _, flag := range []string{"label", "tags", "tag"} {
		cmd := githubTestCommand(createCmd)
		if err := cmd.ParseFlags([]string{"--" + flag, "alpha,beta", "--" + flag, "gamma"}); err != nil {
			t.Fatal(err)
		}
		change, err := gitHubChanges(cmd, true)
		if err != nil || change.Labels == nil || strings.Join(*change.Labels, ",") != "alpha,beta,gamma" {
			t.Fatal(flag, change, err)
		}
	}
	cmd := githubTestCommand(createCmd)
	if err := cmd.ParseFlags([]string{"--labels", "primary", "--tag", "secondary"}); err != nil {
		t.Fatal(err)
	}
	change, err := gitHubChanges(cmd, true)
	if err != nil || change.Labels == nil || strings.Join(*change.Labels, ",") != "primary" {
		t.Fatal("alias precedence", change, err)
	}
	before, err := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, title := range []string{"", "   ", "task", strings.Repeat("a", 10000)} {
		if _, err := executeGitHubTest(githubTestCommand(createCmd), title, "--json"); err == nil {
			t.Fatal("invalid create title", title)
		}
	}
	for _, args := range [][]string{{"6", "--description", "inline", "--description-file", description}, {"6", "--description-file", "-", "--acceptance-file", "-"}, {"6", "--title", ""}} {
		command := githubTestCommand(updateCmd)
		command.SetIn(strings.NewReader("must not write"))
		if _, err := executeGitHubTest(command, args...); err == nil {
			t.Fatal("ambiguous input accepted", args)
		}
	}
	after, _ := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
	if string(before) != string(after) {
		t.Fatal("invalid CRUD preflight wrote")
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("SQLite created")
	}
}

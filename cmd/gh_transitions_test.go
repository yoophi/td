package cmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/ghcontext"
	"github.com/marcus/td/internal/models"
	"github.com/spf13/cobra"
)

func claimTestCommand(original *cobra.Command) *cobra.Command {
	cmd := &cobra.Command{Use: original.Use, Aliases: original.Aliases, Args: original.Args, RunE: original.RunE, SilenceErrors: true, SilenceUsage: true}
	for _, name := range []string{"reason", "session", "stale", "status", "reviewed-by", "decision", "admin", "self-close-exception"} {
		cmd.Flags().String(name, "", "")
	}
	cmd.Flags().Bool("force", false, "")
	cmd.Flags().Bool("all", false, "")
	cmd.Flags().Bool("minor", false, "")
	cmd.Flags().Bool("self-review", false, "")
	cmd.Flags().Bool("record-only", false, "")
	cmd.Flags().Bool("json", false, "")
	return cmd
}

func TestGitHubTransitionRoutingAndFocus(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX gh fixture")
	}
	dir := storeTestDir(t)
	runGit(t, dir, "init")
	runGit(t, dir, "remote", "add", "origin", "https://github.com/owner/repo.git")
	if err := config.SetStore(dir, "gh-issue", &models.GitHubStoreConfig{Repo: "owner/repo", Remote: "origin"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("TD_CONTEXT_ID", "claim-fixture")
	bin := t.TempDir()
	path := filepath.Join(bin, "issue.json")
	if err := os.WriteFile(path, []byte(`{"number":1,"state":"open","title":"Fixture"}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TD_TEST_ISSUE", path)
	script := `#!/bin/sh
if [ "$1" = auth ]; then exit 0; fi
for arg in "$@"; do
 if [ "$arg" = --include ]; then printf 'HTTP/2.0 200 OK\nContent-Type: application/json\r\n\r\n'; break; fi
done
if [ "$4" = repos/owner/repo ]; then
 printf '%s' '{"full_name":"owner/repo","has_issues":true}'
elif [ "$6" = "repos/owner/repo/issues?state=open&per_page=100" ] || [ "$6" = "repos/owner/repo/issues?state=all&per_page=100" ]; then
 printf '[['
 cat "$TD_TEST_ISSUE"
 printf ']]'
elif [ "$6" = "repos/owner/repo/labels?per_page=100" ]; then
 printf '%s' '[[{"name":"td:open"},{"name":"td:in_progress"},{"name":"td:blocked"},{"name":"td:in_review"},{"name":"td:closed"}]]'
elif [ "$6" = "repos/owner/repo/issues/1/events?per_page=100" ]; then
 if grep -q '"state":"closed"' "$TD_TEST_ISSUE"; then
  printf '%s' '[[{"id":1,"event":"closed"}]]'
 else printf '%s' '[[]]'; fi
elif [ "$5" = PATCH ]; then
 input=$(sed -E 's/"labels":\["([^"]*)"\]/"labels":[{"name":"\1"}]/')
 printf '{"number":1,"title":"Fixture",%s' "${input#?}" > "$TD_TEST_ISSUE"
 cat "$TD_TEST_ISSUE"
elif [ "$5" = GET ]; then
 cat "$TD_TEST_ISSUE"
else
 exit 1
fi
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, tc := range []struct {
		command *cobra.Command
		status  string
		noop    bool
	}{
		{startCmd, "in_progress", false}, {startCmd, "in_progress", true}, {blockCmd, "blocked", false}, {unblockCmd, "open", false}, {startCmd, "in_progress", false}, {unstartCmd, "open", false}, {unstartCmd, "open", true},
	} {
		out, err := executeGitHubTest(claimTestCommand(tc.command), "gh-1", "--reason", "test", "--json")
		var result struct {
			Status string `json:"status"`
			Noop   bool   `json:"noop"`
		}
		if err != nil || json.Unmarshal([]byte(out), &result) != nil || result.Status != tc.status || result.Noop != tc.noop {
			t.Fatalf("%s: %s %v", tc.command.Name(), out, err)
		}
		scope, err := ghcontext.Resolve(context.Background(), dir, "owner/repo")
		if err != nil {
			t.Fatal(err)
		}
		local, err := scope.Update(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if tc.command == startCmd && local.Focus != "gh-1" {
			t.Fatal("start did not set focus")
		}
		if tc.command == unstartCmd && local.Focus != "" {
			t.Fatal("unstart did not clear focus")
		}

	}
	for _, tc := range []struct {
		command *cobra.Command
		flags   []string
		status  string
	}{
		{reviewCmd, []string{"--minor"}, "in_review"},
		{approveCmd, []string{"--self-review", "--reason", "Explicit test fixture review"}, "closed"},
		{reopenCmd, nil, "open"},
	} {
		args := append([]string{"gh-1", "--json"}, tc.flags...)
		out, err := executeGitHubTest(claimTestCommand(tc.command), args...)
		var result struct {
			Status string `json:"status"`
		}
		if err != nil || json.Unmarshal([]byte(out), &result) != nil || result.Status != tc.status {
			t.Fatalf("%s: %s %v", tc.command.Name(), out, err)
		}
	}
	for _, status := range []string{"in_progress", "blocked", "open"} {
		out, err := executeGitHubTest(githubTestCommand(updateCmd), "gh-1", "--status", status, "--json")
		var result struct {
			Status string `json:"status"`
		}
		if err != nil || json.Unmarshal([]byte(out), &result) != nil || result.Status != status {
			t.Fatalf("update --status %s: %s %v", status, out, err)
		}
	}
	for _, all := range []bool{false, true} {
		if _, err := executeGitHubTest(claimTestCommand(reviewCmd), "gh-1", "--minor", "--json"); err != nil {
			t.Fatal(err)
		}
		args := []string{"--json", "--reason", "Fixture bulk approval"}
		if all {
			args = append(args, "--all")
		}
		out, err := executeGitHubTest(claimTestCommand(approveCmd), args...)
		var result struct {
			Status string `json:"status"`
		}
		if err != nil || json.Unmarshal([]byte(out), &result) != nil || result.Status != "closed" {
			t.Fatalf("approve all=%v: %s %v", all, out, err)
		}
		if _, err := executeGitHubTest(claimTestCommand(reopenCmd), "gh-1", "--json"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := executeGitHubTest(claimTestCommand(approveCmd), "--all", "gh-1"); err == nil || !strings.Contains(err.Error(), "not both") {
		t.Fatalf("ambiguous bulk args: %v", err)
	}
	started, err := executeGitHubTest(claimTestCommand(startCmd), "gh-1", "--json")
	var startedResult struct {
		Issue models.Issue `json:"issue"`
	}
	if err != nil || json.Unmarshal([]byte(started), &startedResult) != nil {
		t.Fatalf("%s %v", started, err)
	}
	for index, forced := range []bool{false, true, true} {
		args := []string{"--session", startedResult.Issue.ImplementerSession, "--json"}
		if forced {
			args = append(args, "--force")
		}
		out, err := executeGitHubTest(claimTestCommand(unstartCmd), args...)
		var report struct {
			Count  int  `json:"count"`
			Forced bool `json:"forced"`
		}
		if err != nil || json.Unmarshal([]byte(out), &report) != nil || report.Forced != forced {
			t.Fatalf("%s %v", out, err)
		}
		if report.Count != []int{1, 1, 0}[index] {
			t.Fatalf("unexpected release/preview count: %d", report.Count)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("created SQLite DB")
	}
	out, err := executeGitHubTest(claimTestCommand(unstartCmd), "--session", "someone", "--json")
	if err == nil || !strings.Contains(err.Error(), "no known local/shared identity") {
		t.Fatalf("%s %v", out, err)
	}
}

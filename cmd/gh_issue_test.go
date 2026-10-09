package cmd

import (
	"bytes"
	"context"
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

func githubTestCommand(original *cobra.Command) *cobra.Command {
	cmd := &cobra.Command{Use: original.Use, RunE: original.RunE, SilenceErrors: true, SilenceUsage: true}
	for _, name := range []string{"title", "type", "priority", "description", "desc", "body", "notes", "description-file", "acceptance", "acceptance-file", "status", "format", "sort", "search", "reason", "parent", "epic", "comment", "note", "sprint", "due", "defer", "filter", "implementer", "reviewer", "created", "updated", "closed"} {
		if (original == listCmd || original == searchCmd) && (name == "type" || name == "status") {
			continue
		}
		cmd.Flags().String(name, "", "")
	}
	for _, name := range []string{"labels", "label", "tags", "tag", "id", "depends-on", "blocks"} {
		cmd.Flags().StringArray(name, nil, "")
	}
	if original == listCmd || original == searchCmd {
		cmd.Flags().StringArray("type", nil, "")
		cmd.Flags().StringArray("status", nil, "")
	}
	for _, name := range []string{"json", "long", "short", "all", "open", "reverse", "append", "minor", "tree", "children", "render-markdown", "deferred", "overdue", "due-soon", "surfacing", "clear", "mine", "reviewable", "include-approved"} {
		cmd.Flags().Bool(name, false, "")
	}
	if original == queryCmd {
		cmd.Flags().String("output", "table", "")
		cmd.Flags().Int("max-scan", 10000, "")
		for _, name := range []string{"examples", "fields", "explain"} {
			cmd.Flags().Bool(name, false, "")
		}
	}
	cmd.Flags().Bool("show-score", false, "")
	if original == listCmd {
		cmd.Flags().String("points", "", "")
	} else {
		cmd.Flags().Int("points", 0, "")
	}
	cmd.Flags().Int("limit", 50, "")
	return cmd
}

func executeGitHubTest(cmd *cobra.Command, args ...string) (string, error) {
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return out.String(), err
}

func TestGitHubRoutingRejectsUnsupportedBeforeNetwork(t *testing.T) {
	dir := storeTestDir(t)
	if err := config.SetStore(dir, "gh-issue", &models.GitHubStoreConfig{Repo: "owner/repo", Remote: "origin"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	for _, tc := range []struct {
		command *cobra.Command
		args    []string
		want    string
	}{
		{createCmd, []string{"Example issue title", "--parent", "td-invalid"}, "invalid GitHub issue ID"},
		{updateCmd, []string{"gh-1", "--status", "invalid"}, "invalid status"},
		{updateCmd, []string{"gh-1"}, "no issue changes"},
		{showCmd, []string{"td-abcdef"}, "invalid GitHub issue ID"},
		{listCmd, []string{"--sort", "bogus"}, "unsupported gh-issue sort"},
		{listCmd, []string{"unknown_field=open"}, "validation error"},
	} {
		_, err := executeGitHubTest(githubTestCommand(tc.command), tc.args...)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: %v", tc.args, err)
		}
	}
}

func TestGitHubRoutingUsesSelectedRemoteWithoutSQLite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake gh uses POSIX shell")
	}
	dir := storeTestDir(t)
	runGit(t, dir, "init")
	runGit(t, dir, "remote", "add", "origin", "https://github.com/owner/repo.git")
	if err := config.SetStore(dir, "gh-issue", &models.GitHubStoreConfig{Repo: "owner/repo", Remote: "origin"}); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	script := `#!/bin/sh
if [ "$1" = auth ]; then exit 0; fi
for arg in "$@"; do
 if [ "$arg" = --include ]; then printf 'HTTP/2.0 200 OK\nContent-Type: application/json\r\n\r\n'; break; fi
done
if [ "$4" = repos/owner/repo ]; then
  printf '%s' '{"full_name":"owner/repo","has_issues":true}'
elif [ "$6" = 'repos/owner/repo/issues?state=all&per_page=100' ]; then
  printf '%s' '[[{"number":1,"title":"Open issue","state":"open"},{"number":2,"title":"Closed issue","state":"closed"}]]'
elif [ "$6" = 'repos/owner/repo/issues/1/comments?per_page=100' ]; then
  printf '%s' '[[]]'
elif [ "$6" = repos/owner/repo/issues/1 ]; then
  printf '%s' '{"number":1,"title":"Open issue","state":"open","body":"Native GitHub description","html_url":"https://github.com/owner/repo/issues/1"}'
else
  echo "unexpected gh request" >&2
  exit 1
fi
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GH_REPO", "unrelated/project")
	out, err := executeGitHubTest(githubTestCommand(showCmd), "#1", "--json")
	var issue map[string]any
	if err != nil || json.Unmarshal([]byte(out), &issue) != nil || issue["id"] != "gh-1" || issue["description"] != "Native GitHub description" {
		t.Fatalf("out=%s err=%v", out, err)
	}
	out, err = executeGitHubTest(githubTestCommand(listCmd), "--all", "--status", "closed", "--json")
	var issues []map[string]any
	if err != nil || json.Unmarshal([]byte(out), &issues) != nil || len(issues) != 1 || issues[0]["id"] != "gh-2" {
		t.Fatalf("out=%s err=%v", out, err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("created an SQLite issue database")
	}
	// Pinning prevents accidental writes after a user repoints the Git remote.
	if err := config.SetStore(dir, "gh-issue", &models.GitHubStoreConfig{Repo: "owner/previous", Remote: "origin"}); err != nil {
		t.Fatal(err)
	}
	_, err = executeGitHubTest(githubTestCommand(showCmd), "1")
	if err == nil || !strings.Contains(err.Error(), "now resolves to") {
		t.Fatalf("changed repo accepted: %v", err)
	}
}

func TestGitHubInlineCommentAndPartialFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake gh uses POSIX shell")
	}
	for _, tc := range []struct {
		name, flag   string
		change, fail bool
	}{
		{"comment-only", "comment", false, false},
		{"note-only", "note", false, false},
		{"updated-and-commented", "comment", true, false},
		{"partial-failure", "comment", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := storeTestDir(t)
			runGit(t, dir, "init")
			runGit(t, dir, "remote", "add", "origin", "https://github.com/owner/repo.git")
			if err := config.SetStore(dir, "gh-issue", &models.GitHubStoreConfig{Repo: "owner/repo", Remote: "origin"}); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HOME", t.TempDir())
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			bin := t.TempDir()
			statePath := filepath.Join(bin, "issue.json")
			requests := filepath.Join(bin, "requests")
			if err := os.WriteFile(statePath, []byte(`{"number":1,"state":"open","title":"Original"}`), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("TD_TEST_ISSUE", statePath)
			t.Setenv("TD_TEST_REQUESTS", requests)
			if tc.fail {
				t.Setenv("TD_TEST_COMMENT_FAIL", "yes")
			} else {
				t.Setenv("TD_TEST_COMMENT_FAIL", "")
			}
			script := `#!/bin/sh
if [ "$1" = auth ]; then exit 0; fi
for arg in "$@"; do
 if [ "$arg" = --include ]; then printf 'HTTP/2.0 200 OK\nContent-Type: application/json\r\n\r\n'; break; fi
done
if [ "$4" = repos/owner/repo ]; then
 printf '%s' '{"full_name":"owner/repo","has_issues":true}'
 exit 0
fi
printf '%s\n' "$5 $6" >> "$TD_TEST_REQUESTS"
if [ "$5" = PATCH ]; then
 input=$(cat)
 printf '{"number":1,"state":"open",%s' "${input#?}" > "$TD_TEST_ISSUE"
 cat "$TD_TEST_ISSUE"
elif [ "$5" = POST ]; then
 if [ "$TD_TEST_COMMENT_FAIL" = yes ]; then echo forbidden >&2; exit 1; fi
 input=$(cat)
 printf '{"id":99,%s' "${input#?}"
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
			args := []string{"gh-1", "--" + tc.flag, "Comment 한글 $(literal)", "--json"}
			if tc.change {
				args = append(args, "--title", "Changed")
			}
			out, err := executeGitHubTest(githubTestCommand(updateCmd), args...)
			if tc.fail {
				if err == nil || !strings.Contains(err.Error(), "was updated, but its comment failed") || !strings.Contains(err.Error(), "td-op-") {
					t.Fatalf("%s %v", out, err)
				}
			} else if err != nil {
				t.Fatalf("%s %v", out, err)
			}
			calls, err := os.ReadFile(requests)
			if err != nil {
				t.Fatal(err)
			}
			wantPatch := 0
			if tc.change {
				wantPatch = 1
			}
			if strings.Count(string(calls), "PATCH ") != wantPatch || strings.Count(string(calls), "POST ") != 1 {
				t.Fatalf("unexpected calls: %s", calls)
			}
			if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
				t.Fatal("created SQLite DB")
			}
		})
	}
}

func TestGitHubInlineCommentValidatesBeforeMutation(t *testing.T) {
	dir := storeTestDir(t)
	if err := config.SetStore(dir, "gh-issue", &models.GitHubStoreConfig{Repo: "owner/repo", Remote: "origin"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	for _, args := range [][]string{
		{"1", "--title", "Changed", "--comment", " "},
		{"1", "--comment", "one", "--note", "two"},
		{"1", "--comment", "<!-- td:activity:v1"},
	} {
		_, err := executeGitHubTest(githubTestCommand(updateCmd), args...)
		if err == nil || strings.Contains(err.Error(), "CLI not found") {
			t.Fatalf("validation ran after network: %v", err)
		}
	}
}

func TestGitHubMinorAndSprintInputs(t *testing.T) {
	create := githubTestCommand(createCmd)
	if err := create.ParseFlags([]string{"--minor"}); err != nil {
		t.Fatal(err)
	}
	issue, err := newGitHubIssue(create, []string{"Small task"})
	if err != nil || !issue.Minor {
		t.Fatalf("%+v %v", issue, err)
	}
	for _, value := range []string{"sprint-2", ""} {
		update := githubTestCommand(updateCmd)
		if err := update.ParseFlags([]string{"--sprint", value}); err != nil {
			t.Fatal(err)
		}
		changes, err := gitHubChanges(update, false)
		if err != nil || changes.Sprint == nil || *changes.Sprint != value || !hasGitHubChanges(changes) {
			t.Fatalf("%+v %v", changes, err)
		}
	}
}

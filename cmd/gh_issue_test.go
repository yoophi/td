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
	for _, name := range []string{"title", "type", "priority", "description", "desc", "body", "notes", "description-file", "acceptance", "acceptance-file", "status", "format", "sort", "search", "reason", "parent"} {
		if original == listCmd && (name == "type" || name == "status") {
			continue
		}
		cmd.Flags().String(name, "", "")
	}
	for _, name := range []string{"labels", "label", "tags", "tag", "id"} {
		cmd.Flags().StringArray(name, nil, "")
	}
	if original == listCmd {
		cmd.Flags().StringArray("type", nil, "")
		cmd.Flags().StringArray("status", nil, "")
	}
	for _, name := range []string{"json", "long", "short", "all", "open", "reverse", "append"} {
		cmd.Flags().Bool(name, false, "")
	}
	cmd.Flags().Int("points", 0, "")
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
		{createCmd, []string{"Example issue title", "--parent", "gh-2"}, "does not support --parent"},
		{updateCmd, []string{"gh-1", "--status", "in_review"}, "supports only open and closed"},
		{updateCmd, []string{"gh-1"}, "no issue changes"},
		{showCmd, []string{"td-abcdef"}, "invalid GitHub issue ID"},
		{listCmd, []string{"--sort", "bogus"}, "unsupported gh-issue sort"},
		{listCmd, []string{"status=open"}, "does not support positional TDQ"},
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
if [ "$4" = repos/owner/repo ]; then
  printf '%s' '{"full_name":"owner/repo","has_issues":true}'
elif [ "$6" = 'repos/owner/repo/issues?state=all&per_page=100' ]; then
  printf '%s' '[[{"number":1,"title":"Open issue","state":"open"},{"number":2,"title":"Closed issue","state":"closed"}]]'
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

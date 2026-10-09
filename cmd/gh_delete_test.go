package cmd

import (
	"encoding/json"
	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/models"
	"github.com/spf13/cobra"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func deletionTestCommand(original *cobra.Command) *cobra.Command {
	c := &cobra.Command{Use: original.Use, Args: original.Args, RunE: original.RunE, SilenceUsage: true, SilenceErrors: true}
	c.Flags().Bool("json", false, "")
	c.Flags().Bool("force", false, "")
	c.Flags().Bool("yes", false, "")
	c.Flags().String("reason", "", "")
	return c
}
func TestGitHubDeleteRestoreCLIWithoutSQLite(t *testing.T) {
	original := gitHubDeletionSession
	t.Cleanup(func() { gitHubDeletionSession = original })
	gitHubDeletionSession = func(*cobra.Command, string) (string, error) { return "fixture-actual-cli", nil }

	if runtime.GOOS == "windows" {
		t.Skip("fake gh script")
	}
	dir := storeTestDir(t)
	runGit(t, dir, "init")
	runGit(t, dir, "remote", "add", "origin", "https://github.com/owner/repo.git")
	if err := config.SetStore(dir, "gh-issue", &models.GitHubStoreConfig{Repo: "owner/repo", Remote: "origin"}); err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(t.TempDir(), "issue.json")
	if err := os.WriteFile(fixture, []byte(`{"number":1,"title":"Fixture", "state":"open","labels":[{"name":"td:open"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TD_TEST_DELETION_FILE", fixture)
	bin := t.TempDir()
	script := `#!/usr/bin/env python3
import sys,json,os
args=sys.argv[1:]
if args[0]=='auth':sys.exit(0)
if '--include' in args:sys.stdout.write('HTTP/2.0 200 OK\nContent-Type: application/json\r\n\r\n')
if args[3]=='repos/owner/repo':print(json.dumps({'full_name':'owner/repo','has_issues':True}));sys.exit(0)
method,path=args[4:6]
p=os.environ['TD_TEST_DELETION_FILE']
with open(p) as f: issue=json.load(f)
if method=='GET' and '?state=' in path:print(json.dumps([[issue]]))
elif path=='repos/owner/repo/issues/1':
 if method=='PATCH':
  patch=json.load(sys.stdin)
  assert 'state' not in patch
  issue.update(patch)
  with open(p,'w') as f:json.dump(issue,f)
 elif method!='GET':raise Exception('unexpected destructive operation')
 print(json.dumps(issue))
else:
 print('fixture not found (HTTP 404)',file=sys.stderr);sys.exit(1)
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, tc := range []struct {
		command *cobra.Command
		args    []string
		count   int
	}{{deletedCmd, nil, 0}, {deleteCmd, []string{"gh-1", "--force", "--yes", "--reason", " Fixture deletion 이유 \n세부내용 "}, 1}, {deleteCmd, []string{"1"}, 1}, {deletedCmd, nil, 1}, {restoreCmd, []string{"#1", "--reason", " Fixture 복원 \n확인 "}, 1}, {restoreCmd, []string{"gh-1"}, 1}, {deletedCmd, nil, 0}} {
		out, err := executeGitHubTest(deletionTestCommand(tc.command), append(tc.args, "--json")...)
		var data []map[string]any
		if err != nil || json.Unmarshal([]byte(out), &data) != nil || len(data) != tc.count {
			t.Fatalf("%s: %s %v", tc.command.Name(), out, err)
		}
	}

	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	var issue struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal(data, &issue); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(issue.Body, "Fixture deletion 이유") || !strings.Contains(issue.Body, "Fixture 복원") {
		t.Fatalf("reasons missing from shared history: %s", issue.Body)
	}
	before := string(data)
	_, err = executeGitHubTest(deletionTestCommand(deleteCmd), "gh-1", "--reason", "   ")
	if err == nil || !strings.Contains(err.Error(), "nonblank") {
		t.Fatalf("blank reason: %v", err)
	}
	data, err = os.ReadFile(fixture)
	if err != nil || string(data) != before {
		t.Fatal("blank reason wrote")
	}
	_, err = executeGitHubTest(deletionTestCommand(deleteCmd), "gh-1", "gh-2", "--json")
	if err == nil || !strings.Contains(err.Error(), "earlier changes remain") {
		t.Fatalf("partial multi-ID error: %v", err)
	}
	_, err = executeGitHubTest(deletionTestCommand(restoreCmd), "invalid")
	if err == nil || !strings.Contains(err.Error(), "invalid GitHub issue ID") {
		t.Fatalf("invalid ID: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("SQLite DB created")
	}
}

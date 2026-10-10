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
	script := githubJournalFixtureScript(t, `#!/usr/bin/env python3
import sys,json,os,pathlib
args=sys.argv[1:]
if args[0]=='auth':sys.exit(0)
path=next(a for a in args if a.startswith('repos/owner/repo'));method=args[args.index('--method')+1] if '--method' in args else 'GET'
f=pathlib.Path(os.environ['TD_TEST_DELETION_FILE'])
issue=json.loads(f.read_text());issue.setdefault('body','');issue.setdefault('updated_at','2026-10-09T00:00:00Z')
if path=='repos/owner/repo':data={'full_name':'owner/repo','has_issues':True}
elif '/labels?' in path:data=[[{'name':'td:'+v} for v in ['open','in_progress','blocked','in_review','closed']]+[{'name':'td:priority:P'+str(v)} for v in range(5)]]
elif '?state=' in path:data=[[issue]]
elif '/comments?' in path:data=[[]]
elif path=='repos/owner/repo/issues/1':
 if method=='PATCH':
  patch=json.load(sys.stdin);assert 'state' not in patch
  if 'labels' in patch:patch['labels']=[{'name':v} for v in patch['labels']]
  issue.update(patch);issue['updated_at']=fixture_tick(issue['updated_at']);f.write_text(json.dumps(issue))
 elif method!='GET':raise Exception('unexpected destructive operation')
 data=issue
else:sys.stderr.write('fixture not found (HTTP 404)');sys.exit(1)
sys.stdout.write('HTTP/2.0 200 OK\r\nContent-Type: application/json\r\n\r\n'+json.dumps(data))
`)
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
	if err == nil || !strings.Contains(err.Error(), "no write attempted") {
		t.Fatalf("multi-ID preflight error: %v", err)
	}
	data, err = os.ReadFile(fixture)
	if err != nil || string(data) != before {
		t.Fatal("missing later target allowed an earlier deletion")
	}
	_, err = executeGitHubTest(deletionTestCommand(restoreCmd), "invalid")
	if err == nil || !strings.Contains(err.Error(), "invalid GitHub issue ID") {
		t.Fatalf("invalid ID: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("SQLite DB created")
	}
}

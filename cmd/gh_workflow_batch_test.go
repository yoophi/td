package cmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/ghcontext"
	"github.com/marcus/td/internal/models"
)

func TestGitHubWorkflowBatchFocusNDJSONAndPartialFailure(t *testing.T) {
	dir := storeTestDir(t)
	runGit(t, dir, "init")
	runGit(t, dir, "remote", "add", "origin", "https://github.com/owner/repo.git")
	if err := config.SetStore(dir, "gh-issue", &models.GitHubStoreConfig{Remote: "origin", Repo: "owner/repo"}); err != nil {
		t.Fatal(err)
	}
	scope, err := ghcontext.Resolve(context.Background(), dir, "owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(scope.Path) })
	if _, err := scope.Update(context.Background(), func(s *ghcontext.State) error { s.Focus = "gh-99"; return nil }); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	t.Setenv("TD_BATCH_STATE", filepath.Join(bin, "issues.json"))
	script := `#!/usr/bin/env python3
import sys,os,json,pathlib
args=sys.argv[1:]
if args[0]=='auth':sys.exit(0)
path=next(a for a in args if a.startswith('repos/owner/repo'))
method=args[args.index('--method')+1] if '--method' in args else 'GET'
f=pathlib.Path(os.environ['TD_BATCH_STATE'])
issues=json.loads(f.read_text()) if f.exists() else {str(n):{'number':n,'title':'Fixture','state':'open','body':'','labels':[]} for n in [1,2]}
if path=='repos/owner/repo':data={'full_name':'owner/repo','has_issues':True}
elif '/comments?' in path:data=[[]]
elif '/labels?' in path:data=[[{'name':'td:'+s} for s in ['open','in_progress','blocked','in_review','closed']]]
elif path.startswith('repos/owner/repo/issues?'):data=[list(issues.values())]
else:
 n=path.split('/issues/')[1]
 if method=='PATCH':
  if os.environ.get('TD_BATCH_FAIL')==n:sys.stderr.write('permission denied');sys.exit(1)
  patch=json.load(sys.stdin)
  if 'labels' in patch:patch['labels']=[{'name':name} for name in patch['labels']]
  issues[n].update(patch);f.write_text(json.dumps(issues))
 data=issues[n]
sys.stdout.write('HTTP/2.0 200 OK\r\nContent-Type: application/json\r\n\r\n'+json.dumps(data))
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := executeGitHubTest(claimTestCommand(startCmd), "gh-1", "gh-2", "--json")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if err != nil || len(lines) != 2 {
		t.Fatal(out, err)
	}
	for _, line := range lines {
		if !json.Valid([]byte(line)) {
			t.Fatal("invalid NDJSON", out)
		}
	}
	state, _ := scope.Update(context.Background(), nil)
	if state.Focus != "gh-99" {
		t.Fatal("batch selected an arbitrary focus", state)
	}
	// A no-op must not change the previous selection either.
	if _, err := executeGitHubTest(claimTestCommand(startCmd), "gh-1", "--json"); err != nil {
		t.Fatal(err)
	}
	state, _ = scope.Update(context.Background(), nil)
	if state.Focus != "gh-99" {
		t.Fatal("no-op changed focus", state)
	}
	if _, err := executeGitHubTest(claimTestCommand(unstartCmd), "gh-1", "gh-2", "--json"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TD_BATCH_FAIL", "2")
	out, err = executeGitHubTest(claimTestCommand(startCmd), "gh-1", "gh-2", "--json")
	if err == nil || !strings.Contains(err.Error(), "completed for gh-1") || !strings.Contains(err.Error(), "earlier changes remain") || !json.Valid([]byte(strings.TrimSpace(out))) {
		t.Fatal("partial batch evidence", out, err)
	}
	for id, expected := range map[string]string{"gh-1": "in_progress", "gh-2": "open"} {
		out, err := executeGitHubTest(githubTestCommand(showCmd), id, "--json")
		var record struct {
			Status string `json:"status"`
		}
		if err != nil || json.Unmarshal([]byte(out), &record) != nil || record.Status != expected {
			t.Fatalf("actual state for %s: %s %v", id, out, err)
		}
	}

	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("SQLite created")
	}
}

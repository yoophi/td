package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
	"github.com/spf13/cobra"
)

func githubRelationshipCommand(original *cobra.Command) *cobra.Command {
	c := doctorTestCommand(original)
	c.Flags().String("depends-on", "", "")
	c.Flags().Bool("blocking", false, "")
	c.Flags().Bool("direct", false, "")
	c.Flags().Int("depth", 0, "")
	c.Flags().Int("limit", 10, "")
	return c
}

func TestGitHubRelationshipCommandsGraphAndPartialWrites(t *testing.T) {
	dir := storeTestDir(t)
	runGit(t, dir, "init")
	runGit(t, dir, "remote", "add", "origin", "https://github.com/owner/repo.git")
	if err := config.SetStore(dir, "gh-issue", &models.GitHubStoreConfig{Remote: "origin", Repo: "owner/repo"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TD_CONTEXT_ID", "relationship-fixture")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	bin := t.TempDir()
	t.Setenv("TD_REL_STATE", filepath.Join(bin, "issues.json"))
	script := `#!/usr/bin/env python3
import sys,os,json,pathlib
args=sys.argv[1:]
if args[0]=='auth':sys.exit(0)
path=next(a for a in args if a.startswith('repos/owner/repo'))
method=args[args.index('--method')+1] if '--method' in args else 'GET'
f=pathlib.Path(os.environ['TD_REL_STATE'])
def issue(n,typ='task',details=None):
 return {'number':n,'title':'Fixture '+str(n),'state':'open','body':'<!-- td:issue:v1\n'+json.dumps({'type':typ,'priority':'P2','points':0,'details':details or {}})+'\n-->','labels':[{'name':'td:open'}]}
issues=json.loads(f.read_text()) if f.exists() else {'1':issue(1,'epic'),'2':issue(2,details={'parent_id':'gh-1','dependencies':['gh-3']}),'3':issue(3),'4':dict(issue(4),pull_request={'url':'https://example.test/pr'})}
if path=='repos/owner/repo':data={'full_name':'owner/repo','has_issues':True}
elif '/labels?' in path:data=[[{'name':'td:'+s} for s in ['open','in_progress','blocked','in_review','closed']]]
elif path.startswith('repos/owner/repo/issues?'):
 data=[[v for k,v in issues.items() if k!='4' and (k!='3' or not os.environ.get('TD_REL_LAG'))]]
else:
 n=path.split('/issues/')[1]
 if n not in issues:sys.stderr.write('HTTP 404 issue not found');sys.exit(1)
 if method=='PATCH':
  if os.environ.get('TD_REL_FAIL')==n:sys.stderr.write('HTTP 403 permission denied');sys.exit(1)
  patch=json.load(sys.stdin)
  if 'labels' in patch:patch['labels']=[{'name':v} for v in patch['labels']]
  issues[n].update(patch);f.write_text(json.dumps(issues))
 data=issues[n]
sys.stdout.write('HTTP/2.0 200 OK\r\nContent-Type: application/json\r\n\r\n'+json.dumps(data))
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	execute := func(original *cobra.Command, args ...string) string {
		t.Helper()
		out, err := executeGitHubTest(githubRelationshipCommand(original), append(args, "--json")...)
		if err != nil {
			t.Fatal(out, err)
		}
		if !json.Valid([]byte(out)) {
			t.Fatal("invalid JSON", out)
		}
		return out
	}
	t.Setenv("TD_REL_LAG", "1")
	out := execute(dependsOnCmd, "#2")
	if !strings.Contains(out, `"dependencies":["gh-3"]`) {
		t.Fatal(out)
	}
	out = execute(blockedByCmd, "3")
	if !strings.Contains(out, `"direct":["gh-2"]`) || !strings.Contains(out, `"all":["gh-2"]`) {
		t.Fatal(out)
	}
	out = execute(depCmd, "gh-3", "--blocking")
	if !strings.Contains(out, `"blocked":["gh-2"]`) {
		t.Fatal(out)
	}
	out = execute(treeCmd, "gh-1", "--depth", "1")
	if !strings.Contains(out, `"id":"gh-2"`) || !strings.Contains(out, `"children":[]`) {
		t.Fatal(out)
	}
	out = execute(criticalPathCmd)
	if !strings.Contains(out, `"critical_path":["gh-3","gh-2"]`) || !strings.Contains(out, `"ready_to_start":["gh-3"]`) || !strings.Contains(out, `"score":1`) {
		t.Fatal(out)
	}
	out = execute(depAddCmd, "gh-2", "--depends-on", "gh-3")
	if !strings.Contains(out, `"already_exists":true`) {
		t.Fatal(out)
	}
	before, err := os.ReadFile(os.Getenv("TD_REL_STATE"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, target := range []string{"owner/other#3", "gh-2", "gh-4", "gh-99"} {
		out, err := executeGitHubTest(githubRelationshipCommand(depAddCmd), "gh-2", target, "--json")
		if err == nil {
			t.Fatal("invalid relationship accepted", target, out)
		}
		after, _ := os.ReadFile(os.Getenv("TD_REL_STATE"))
		if string(before) != string(after) {
			t.Fatal("invalid relationship changed issue", target)
		}
	}
	out, err = executeGitHubTest(githubRelationshipCommand(depAddCmd), "gh-2", "gh-1", "gh-99", "--json")
	if err == nil || !strings.Contains(err.Error(), "completed targets gh-1") || !strings.Contains(err.Error(), "earlier changes remain") || !json.Valid([]byte(strings.TrimSpace(out))) {
		t.Fatal(out, err)
	}
	out = execute(dependsOnCmd, "gh-2")
	if !strings.Contains(out, `"dependencies":["gh-3","gh-1"]`) {
		t.Fatal(out)
	}
	execute(depRmCmd, "gh-2", "gh-1")
	t.Setenv("TD_REL_FAIL", "2")
	if _, err := executeGitHubTest(githubRelationshipCommand(depRmCmd), "gh-2", "gh-3", "--json"); err == nil {
		t.Fatal("permission error swallowed")
	}
	out = execute(dependsOnCmd, "gh-2")
	if !strings.Contains(out, `"dependencies":["gh-3"]`) {
		t.Fatal(out)
	}
	human, err := executeGitHubTest(githubRelationshipCommand(criticalPathCmd))
	if err != nil || !strings.Contains(human, "START NOW:") || !strings.Contains(human, "BOTTLENECKS:") {
		t.Fatal(human, err)
	}
	// A manually corrupted dependency graph must not silently produce a
	// misleading ready sequence, and depth limits must not hide parent cycles.
	data, err := os.ReadFile(os.Getenv("TD_REL_STATE"))
	if err != nil {
		t.Fatal(err)
	}
	var rows map[string]map[string]any
	if err := json.Unmarshal(data, &rows); err != nil {
		t.Fatal(err)
	}
	rows["3"]["body"] = "<!-- td:issue:v1\n{\"type\":\"task\",\"priority\":\"P2\",\"points\":0,\"details\":{\"dependencies\":[\"gh-2\"]}}\n-->"
	data, err = json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("TD_REL_STATE"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := executeGitHubTest(githubRelationshipCommand(criticalPathCmd), "--json"); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatal("cycle hidden", err)
	}
	rows["3"]["body"] = ""
	rows["1"]["body"] = "<!-- td:issue:v1\n{\"type\":\"epic\",\"priority\":\"P2\",\"points\":0,\"details\":{\"parent_id\":\"gh-2\"}}\n-->"
	data, err = json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("TD_REL_STATE"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := executeGitHubTest(githubRelationshipCommand(treeCmd), "gh-1", "--depth", "1", "--json"); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatal("parent cycle hidden by depth", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("SQLite created")
	}
}

func TestGitHubCriticalPathKeepsInReviewDependencyUnresolved(t *testing.T) {
	g := &githubRelationshipGraph{ids: []string{"gh-1", "gh-2"}, records: map[string]ghstore.Record{
		"gh-1": {Issue: models.Issue{ID: "gh-1", Type: models.TypeTask, Status: models.StatusInReview, Priority: models.PriorityP2}},
		"gh-2": {Issue: models.Issue{ID: "gh-2", Type: models.TypeTask, Status: models.StatusOpen, Priority: models.PriorityP2}, Details: &ghstore.IssueDetails{Dependencies: []string{"gh-1"}}},
	}}
	c := &cobra.Command{RunE: func(cmd *cobra.Command, _ []string) error { return emitGitHubCriticalPath(cmd, g, 10) }}
	c.Flags().Bool("json", false, "")
	out, err := executeGitHubTest(c, "--json")
	if err != nil || !strings.Contains(out, `"critical_path":[]`) || !strings.Contains(out, `"ready_to_start":[]`) {
		t.Fatal("review dependency counted as resolved", out, err)
	}
	row := g.records["gh-1"]
	row.Status = models.StatusClosed
	g.records["gh-1"] = row
	c = &cobra.Command{RunE: func(cmd *cobra.Command, _ []string) error { return emitGitHubCriticalPath(cmd, g, 10) }}
	c.Flags().Bool("json", false, "")
	out, err = executeGitHubTest(c, "--json")
	if err != nil || !strings.Contains(out, `"critical_path":["gh-2"]`) {
		t.Fatal("closed dependency not resolved", out, err)
	}
}

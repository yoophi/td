package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/models"
	"github.com/spf13/cobra"
)

func TestGitHubCreateUpdateRelationsReplacePreflightAndPartialFailures(t *testing.T) {
	dir := storeTestDir(t)
	runGit(t, dir, "init")
	runGit(t, dir, "remote", "add", "origin", "https://github.com/owner/repo.git")
	if err := config.SetStore(dir, "gh-issue", &models.GitHubStoreConfig{Remote: "origin", Repo: "owner/repo"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TD_CONTEXT_ID", "relation-options-fixture")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	bin := t.TempDir()
	t.Setenv("TD_RO_STATE", filepath.Join(bin, "issues.json"))
	script := `#!/usr/bin/env python3
import sys,os,json,pathlib
args=sys.argv[1:]
if args[0]=='auth':sys.exit(0)
path=next(a for a in args if a.startswith('repos/owner/repo'));method=args[args.index('--method')+1] if '--method' in args else 'GET'
f=pathlib.Path(os.environ['TD_RO_STATE'])
def issue(n):return {'number':n,'title':'Fixture '+str(n),'state':'open','labels':[{'name':'td:open'}],'body':'<!-- td:issue:v1\n'+json.dumps({'type':'task','priority':'P2','points':0,'details':{'minor':True}})+'\n-->'}
issues=json.loads(f.read_text()) if f.exists() else {str(n):issue(n) for n in [1,2,3]}
if path=='repos/owner/repo':data={'full_name':'owner/repo','has_issues':True}
elif '/comments?' in path:data=[[]]
elif '/labels?' in path:data=[[{'name':'td:'+v} for v in ['open','in_progress','blocked','in_review','closed']]]
elif path.startswith('repos/owner/repo/issues?'):
 trigger=os.environ.get('TD_RO_MEMBERSHIP')
 if trigger:
  counter=f.with_suffix('.reads');reads=int(counter.read_text())+1 if counter.exists() else 1;counter.write_text(str(reads))
  if reads==int(trigger):
   issues['3']['body']='<!-- td:issue:v1\n'+json.dumps({'type':'task','priority':'P2','points':0,'details':{'minor':True,'dependencies':['gh-4']}})+'\n-->';f.write_text(json.dumps(issues))
 data=[list(issues.values())]
elif path=='repos/owner/repo/issues' and method=='POST':
 n=str(max(map(int,issues))+1);patch=json.load(sys.stdin);patch['labels']=[{'name':v} for v in patch['labels']];data=issue(int(n));data.update(patch);issues[n]=data;f.write_text(json.dumps(issues))
elif '/events?' in path:data=[[]]
else:
 n=path.split('/issues/')[1]
 if n not in issues:sys.stderr.write('HTTP 404 missing issue');sys.exit(1)
 if method=='PATCH':
  if os.environ.get('TD_RO_FAIL')==n:sys.stderr.write('HTTP 403 permission denied');sys.exit(1)
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
		out, err := executeGitHubTest(githubTestCommand(original), append(args, "--json")...)
		if err != nil || !json.Valid([]byte(out)) {
			t.Fatal(out, err)
		}
		return out
	}
	deps := func(id string) []string {
		t.Helper()
		var d struct {
			Details struct {
				Dependencies []string `json:"dependencies"`
			} `json:"details"`
		}
		if err := json.Unmarshal([]byte(execute(showCmd, id)), &d); err != nil {
			t.Fatal(err)
		}
		return d.Details.Dependencies
	}
	if out := execute(createCmd, "Relation fixture creation", "--parent", "1", "--depends-on", "3", "--blocks", "2", "--minor"); !strings.Contains(out, `"id":"gh-4"`) {
		t.Fatal(out)
	}
	if strings.Join(deps("4"), ",") != "gh-3" || strings.Join(deps("2"), ",") != "gh-4" {
		t.Fatal("create relations lost")
	}
	execute(updateCmd, "4", "--depends-on", "1", "--blocks", "3")
	if strings.Join(deps("4"), ",") != "gh-1" || len(deps("2")) != 0 || strings.Join(deps("3"), ",") != "gh-4" {
		t.Fatal("replacement lost")
	}
	before, _ := os.ReadFile(os.Getenv("TD_RO_STATE"))
	for _, target := range []string{"3", "gh-999", "owner/other#1"} {
		if _, err := executeGitHubTest(githubTestCommand(updateCmd), "4", "--title", "Must not save this invalid change", "--depends-on", target, "--json"); err == nil {
			t.Fatal("invalid replacement accepted", target)
		}
		after, _ := os.ReadFile(os.Getenv("TD_RO_STATE"))
		if string(after) != string(before) {
			t.Fatal("preflight failure changed fields")
		}
	}
	execute(updateCmd, "4", "--depends-on", "", "--blocks", "")
	execute(updateCmd, "1", "--depends-on", "2")
	execute(updateCmd, "1", "--depends-on", "", "--blocks", "2")
	if len(deps("1")) != 0 || strings.Join(deps("2"), ",") != "gh-1" {
		t.Fatal("first edge reversal")
	}
	execute(updateCmd, "1", "--depends-on", "2", "--blocks", "")
	if len(deps("2")) != 0 || strings.Join(deps("1"), ",") != "gh-2" {
		t.Fatal("safe order for edge reversal")
	}
	// Relation edits must be applied before evaluating a requested approval.
	if _, err := executeGitHubTest(claimTestCommand(reviewCmd), "1", "--minor", "--json"); err != nil {
		t.Fatal(err)
	}
	if _, err := executeGitHubTest(githubTestCommand(updateCmd), "1", "--depends-on", "3", "--status", "closed", "--json"); err == nil || !strings.Contains(err.Error(), "changes were saved") {
		t.Fatal("approval used old relation basis", err)
	}
	var shown map[string]any
	if err := json.Unmarshal([]byte(execute(showCmd, "1")), &shown); err != nil {
		t.Fatal(err)
	}
	if shown["status"] != "in_review" || strings.Join(deps("1"), ",") != "gh-3" {
		t.Fatal("incorrect actual remaining state", shown)
	}
	execute(updateCmd, "1", "--depends-on", "")
	t.Setenv("TD_RO_FAIL", "3")
	_, err := executeGitHubTest(githubTestCommand(updateCmd), "1", "--blocks", "2,3", "--json")
	if err == nil || !strings.Contains(err.Error(), "completed sources=[gh-2]") || !strings.Contains(err.Error(), "earlier") {
		t.Fatal("partial replacement hidden", err)
	}
	if strings.Join(deps("2"), ",") != "gh-1" || len(deps("3")) != 0 {
		t.Fatal("unexpected partial state")
	}
	t.Setenv("TD_RO_FAIL", "2")
	_, err = executeGitHubTest(githubTestCommand(createCmd), "Partial creation fixture", "--blocks", "2", "--json")
	if err == nil || !strings.Contains(err.Error(), "issue gh-5 was created") {
		t.Fatal("created issue identity hidden", err)
	}
	if !strings.Contains(execute(showCmd, "5"), "Partial creation fixture") {
		t.Fatal("created issue lost")
	}
	t.Setenv("TD_RO_FAIL", "")
	_, err = executeGitHubTest(githubTestCommand(updateCmd), "4", "3", "--depends-on", "3", "--json")
	if err == nil || !strings.Contains(err.Error(), "completed issues=[gh-4]") || !strings.Contains(err.Error(), "earlier changes remain") {
		t.Fatal("multi-issue partial failure hidden", err)
	}
	if strings.Join(deps("4"), ",") != "gh-3" || len(deps("3")) != 0 {
		t.Fatal("multi-issue actual state")
	}
	for _, trigger := range []string{"2", "3"} {
		data, err := os.ReadFile(os.Getenv("TD_RO_STATE"))
		if err != nil {
			t.Fatal(err)
		}
		var rows map[string]map[string]any
		if err := json.Unmarshal(data, &rows); err != nil {
			t.Fatal(err)
		}
		rows["3"]["body"] = ""
		data, err = json.Marshal(rows)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(os.Getenv("TD_RO_STATE"), data, 0600); err != nil {
			t.Fatal(err)
		}
		_ = os.Remove(strings.TrimSuffix(os.Getenv("TD_RO_STATE"), ".json") + ".reads")
		t.Setenv("TD_RO_MEMBERSHIP", trigger)
		_, err = executeGitHubTest(githubTestCommand(updateCmd), "4", "--blocks", "2", "--json")
		if err == nil || !strings.Contains(err.Error(), "membership") || !strings.Contains(err.Error(), "earlier") {
			t.Fatal("membership race hidden", trigger, err)
		}
		if strings.Join(deps("3"), ",") != "gh-4" {
			t.Fatal("lost external membership change")
		}
		if trigger == "2" && strings.Contains(strings.Join(deps("2"), ","), "gh-4") {
			t.Fatal("membership conflict wrote own edge")
		}
		if trigger == "3" && !strings.Contains(strings.Join(deps("2"), ","), "gh-4") {
			t.Fatal("post-write conflict hid applied edge")
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("SQLite created")
	}
}

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
	"github.com/spf13/cobra"
)

func TestGitHubShowAndListHierarchyClosedParentsAndContextFallbacks(t *testing.T) {
	dir := storeTestDir(t)
	runGit(t, dir, "init")
	runGit(t, dir, "remote", "add", "origin", "https://github.com/owner/repo.git")
	if err := config.SetStore(dir, "gh-issue", &models.GitHubStoreConfig{Remote: "origin", Repo: "owner/repo"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TD_CONTEXT_ID", "hierarchy-fixture")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	bin := t.TempDir()
	script := `#!/usr/bin/env python3
import sys,os,json
args=sys.argv[1:]
if args[0]=='auth':sys.exit(0)
path=next(a for a in args if a.startswith('repos/owner/repo'))
def issue(n):
 parent={2:'gh-1',3:'gh-2',5:'gh-4',6:'gh-1'}.get(n,'')
 if os.environ.get('TD_HIER_CYCLE') and n==1:parent='gh-2'
 if os.environ.get('TD_HIER_MISSING') and n==1:parent='gh-99'
 details={'parent_id':parent}
 if n==2 and os.environ.get('TD_HIER_OWNER'):details.update(status=os.environ.get('TD_HIER_STATUS','in_progress'),implementer_session=os.environ['TD_HIER_OWNER'])
 return {'number':n,'title':'Fixture '+str(n),'state':'closed' if n in [1,6] else 'open','body':'<!-- td:issue:v1\n'+json.dumps({'type':'epic' if n in [1,4] else 'task','priority':'P2','points':0,'details':details})+'\n-->','labels':[{'name':'td:closed' if n in [1,6] else 'td:open'}]}
if path=='repos/owner/repo':data={'full_name':'owner/repo','has_issues':True}
elif path.startswith('repos/owner/repo/issues?'):data=[[issue(n) for n in range(1,7)]]
elif '/comments?' in path:
 n=int(path.split('/issues/')[1].split('/')[0]);actor=os.environ.get('TD_HIER_LOG_'+str(n),'')
 if os.environ.get('TD_HIER_FAIL_COMMENTS'):sys.stderr.write('HTTP 403 comment read denied');sys.exit(1)
 body='progress\n\n<!-- td:activity:v1\n'+json.dumps({'kind':'log','operation_id':'td-op-fixture-'+str(n),'session_id':actor,'message':'progress','log_type':'progress'},separators=(',',':'))+'\n-->'
 data=[[{'id':100+n,'body':body,'created_at':'2026-10-09T00:00:00Z','updated_at':'2026-10-09T00:00:00Z','user':{'login':'synthetic-fixture'}}]] if actor else [[]]
else:
 n=int(path.split('/issues/')[1])
 if n==7:data=dict(issue(n),pull_request={'url':'https://example.test/pr'})
 elif n not in range(1,7):sys.stderr.write('HTTP 404 missing issue');sys.exit(1)
 else:data=issue(n)
sys.stdout.write('HTTP/2.0 200 OK\r\nContent-Type: application/json\r\n\r\n'+json.dumps(data))
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	scope, err := ghcontext.Resolve(context.Background(), dir, "owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(scope.Path) })
	state, err := scope.Update(context.Background(), nil)
	if err != nil {
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
	listed := func(args ...string) string {
		t.Helper()
		var rows []struct {
			ID string `json:"id"`
		}
		out := execute(listCmd, args...)
		if err := json.Unmarshal([]byte(out), &rows); err != nil {
			t.Fatal(err)
		}
		ids := []string{}
		for _, r := range rows {
			ids = append(ids, r.ID)
		}
		return strings.Join(ids, ",")
	}
	if got := listed("--parent", "1"); got != "gh-2" {
		t.Fatal(got)
	}
	if got := listed("--epic", "#1"); got != "gh-2,gh-3" {
		t.Fatal(got)
	}
	if got := listed("--epic", "1", "--all"); got != "gh-2,gh-3,gh-6" {
		t.Fatal(got)
	}
	if got := listed("--parent", "2", "--epic", "1"); got != "gh-3" {
		t.Fatal(got)
	}
	var tree struct {
		Children []struct {
			ID       string `json:"id"`
			Children []struct {
				ID string `json:"id"`
			} `json:"children"`
		} `json:"children"`
	}
	if err := json.Unmarshal([]byte(execute(showCmd, "1", "--tree")), &tree); err != nil {
		t.Fatal(err)
	}
	if len(tree.Children) != 2 || tree.Children[0].ID != "gh-2" || tree.Children[0].Children[0].ID != "gh-3" {
		t.Fatal(tree)
	}
	out := execute(showCmd, "1", "--children")
	if !strings.Contains(out, `"children":[`) || !strings.Contains(out, `"id":"gh-6"`) {
		t.Fatal(out)
	}
	if out = execute(showCmd, "3", "--children"); !strings.Contains(out, `"children":[]`) {
		t.Fatal(out)
	}
	out = execute(showCmd, "1", "3", "--children")
	if !strings.HasPrefix(strings.TrimSpace(out), "[") {
		t.Fatal(out)
	}
	human, err := executeGitHubTest(githubTestCommand(showCmd), "1")
	if err != nil || !strings.Contains(human, "CHILDREN:") || !strings.Contains(human, "gh-6") {
		t.Fatal(human, err)
	}
	if _, err := scope.Update(context.Background(), func(s *ghcontext.State) error { s.Focus = "gh-1"; return nil }); err != nil {
		t.Fatal(err)
	}
	if got := listed("--epic", "."); got != "gh-2,gh-3" {
		t.Fatal(got)
	}
	if got := listed("--parent", "."); got != "gh-2" {
		t.Fatal(got)
	}
	if _, err := scope.Update(context.Background(), func(s *ghcontext.State) error { s.Focus = ""; return nil }); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TD_HIER_OWNER", state.Session.ID)
	if got := listed("--epic", "."); got != "gh-2,gh-3" {
		t.Fatal("owned work root", got)
	}
	t.Setenv("TD_HIER_STATUS", "in_review")
	if got := listed("--epic", "."); got != "gh-2,gh-3" {
		t.Fatal("owned review root", got)
	}
	t.Setenv("TD_HIER_OWNER", "")
	t.Setenv("TD_HIER_LOG_2", state.Session.ID)
	if got := listed("--epic", "."); got != "gh-2,gh-3" {
		t.Fatal("shared log fallback", got)
	}
	t.Setenv("TD_HIER_LOG_5", state.Session.ID)
	if _, err := executeGitHubTest(githubTestCommand(listCmd), "--epic", ".", "--json"); err == nil || !strings.Contains(err.Error(), "multiple issue roots") {
		t.Fatal("ambiguous history accepted", err)
	}
	if _, err := scope.Update(context.Background(), func(s *ghcontext.State) error {
		_, err := s.StartWorkSession("synthetic bundle", "", dir, dir)
		if err != nil {
			return err
		}
		return s.TagWorkSession("gh-2")
	}); err != nil {
		t.Fatal(err)
	}
	if got := listed("--epic", "."); got != "gh-2,gh-3" {
		t.Fatal("work bundle fallback", got)
	}
	t.Setenv("TD_HIER_FAIL_COMMENTS", "1")
	if _, err := executeGitHubTest(githubTestCommand(listCmd), "--epic", ".", "--json"); err == nil || !strings.Contains(err.Error(), "activity") {
		t.Fatal("history failure silently fell back", err)
	}
	t.Setenv("TD_HIER_FAIL_COMMENTS", "")
	if _, err := scope.Update(context.Background(), func(s *ghcontext.State) error { s.Focus = "gh-99"; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := executeGitHubTest(githubTestCommand(listCmd), "--parent", ".", "--json"); err == nil || !strings.Contains(err.Error(), "saved focus") {
		t.Fatal("stale focus silently replaced", err)
	}
	for _, flag := range []string{"parent", "epic"} {
		if _, err := executeGitHubTest(githubTestCommand(listCmd), "--"+flag, "7", "--json"); err == nil {
			t.Fatal("PR accepted")
		}
	}
	t.Setenv("TD_HIER_CYCLE", "1")
	if _, err := executeGitHubTest(githubTestCommand(listCmd), "--epic", "1", "--json"); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatal("cycle hidden", err)
	}
	t.Setenv("TD_HIER_CYCLE", "")
	t.Setenv("TD_HIER_MISSING", "1")
	if _, err := executeGitHubTest(githubTestCommand(showCmd), "1", "--children", "--json"); err == nil {
		t.Fatal("missing parent hidden")
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("SQLite created")
	}
}

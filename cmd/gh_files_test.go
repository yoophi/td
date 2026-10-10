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

func githubFilesCommand(original *cobra.Command) *cobra.Command {
	c := doctorTestCommand(original)
	c.Flags().String("role", "implementation", "")
	c.Flags().String("depends-on", "", "")
	c.Flags().Bool("recursive", true, "")
	c.Flags().Bool("changed", false, "")
	c.Flags().Bool("untracked", false, "")
	return c
}
func TestGitHubFilesGlobRolesStatusAndPartialCommentFailure(t *testing.T) {
	dir := storeTestDir(t)
	runGit(t, dir, "init")
	runGit(t, dir, "remote", "add", "origin", "https://github.com/owner/repo.git")
	if err := config.SetStore(dir, "gh-issue", &models.GitHubStoreConfig{Remote: "origin", Repo: "owner/repo"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TD_CONTEXT_ID", "files-fixture")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	bin := t.TempDir()
	t.Setenv("TD_FILE_STATE", filepath.Join(bin, "state.json"))
	script := `#!/usr/bin/env python3
import sys,os,json,pathlib
args=sys.argv[1:]
if args[0]=='auth':sys.exit(0)
path=next(a for a in args if a.startswith('repos/owner/repo'))
method=args[args.index('--method')+1] if '--method' in args else 'GET'
f=pathlib.Path(os.environ['TD_FILE_STATE']);s=json.loads(f.read_text()) if f.exists() else {'issue':{'number':1,'title':'Fixture','state':'open','updated_at':'2026-10-09T00:00:00Z','body':'','labels':[{'name':'td:open'}]},'comments':[]}
if path=='repos/owner/repo':data={'full_name':'owner/repo','has_issues':True}
elif '/issues?' in path:data=[[s['issue']]]
elif '/comments?' in path:data=[s['comments']]
elif '/labels?' in path:data=[[{'name':'td:'+v} for v in ['open','in_progress','blocked','in_review','closed']]+[{'name':'td:priority:P'+str(v)} for v in range(5)]]
elif path.endswith('/comments') and method=='POST':
 if os.environ.get('TD_FILE_FAIL_COMMENT'):sys.stderr.write('HTTP 403 comment denied');sys.exit(1)
 clock=fixture_tick(max(s['issue']['updated_at'],journal['comments'][-1]['created_at'] if journal['comments'] else s['issue']['updated_at']))
 comment_id=s.get('next_comment_id',20000);s['next_comment_id']=comment_id+1
 data={'id':comment_id,'body':json.load(sys.stdin)['body'],'created_at':clock,'updated_at':clock,'user':{'login':'fixture-author'},'issue_url':'https://api.github.com/repos/owner/repo/issues/1'};s['comments'].append(data);s['issue']['updated_at']=clock;f.write_text(json.dumps(s))
elif path.startswith('repos/owner/repo/issues/comments/') and method=='GET':
 n=int(path.rsplit('/',1)[1]);data=next((c for c in s['comments'] if c['id']==n),None)
 if data is None:sys.stderr.write('HTTP 404 comment missing');sys.exit(1)
else:
 if method=='PATCH':
  if os.environ.get('TD_FILE_FAIL_WRITE'):sys.stderr.write('HTTP 403 write denied');sys.exit(1)
  patch=json.load(sys.stdin)
  if 'labels' in patch:patch['labels']=[{'name':v} for v in patch['labels']]
  s['issue'].update(patch);s['issue']['updated_at']=fixture_tick(s['issue']['updated_at']);f.write_text(json.dumps(s))
 data=s['issue']
sys.stdout.write('HTTP/2.0 200 OK\r\nContent-Type: application/json\r\n\r\n'+json.dumps(data))
`
	script = githubJournalFixtureScript(t, script)
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, path)), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, path), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("src/one.go", "one\n")
	write("src/nested/two.go", "two\n")
	write("not-linked space.go", "unlinked\n")
	execute := func(original *cobra.Command, args ...string) string {
		t.Helper()
		out, err := executeGitHubTest(githubFilesCommand(original), append(args, "--json")...)
		if err != nil || !json.Valid([]byte(out)) {
			t.Fatal(out, err)
		}
		return out
	}
	out := execute(linkCmd, "1", filepath.Join(dir, "src"), "--recursive=false", "--role", "test")
	if !strings.Contains(out, `"count":1`) {
		t.Fatal(out)
	}
	out = execute(filesCmd, "1")
	if !strings.Contains(out, `"status":"unchanged"`) || !strings.Contains(out, `"role":"test"`) || strings.Contains(out, "two.go") {
		t.Fatal(out)
	}
	if out = execute(linkCmd, "1", filepath.Join(dir, "src/one.go"), "--role", "test"); !strings.Contains(out, `"count":0`) {
		t.Fatal("duplicate refresh wrote", out)
	}
	execute(linkCmd, "1", filepath.Join(dir, "src"), "--role", "implementation")
	write("src/one.go", "modified\n")
	if err := os.Remove(filepath.Join(dir, "src/nested/two.go")); err != nil {
		t.Fatal(err)
	}
	out = execute(filesCmd, "1", "--changed")
	if !strings.Contains(out, `"status":"modified"`) || !strings.Contains(out, `"status":"deleted"`) {
		t.Fatal(out)
	}
	out = execute(filesCmd, "1", "--untracked")
	if !strings.Contains(out, "not-linked space.go") || !strings.Contains(out, `"unlinked":true`) {
		t.Fatal(out)
	}
	before, _ := os.ReadFile(os.Getenv("TD_FILE_STATE"))
	outside := filepath.Join(t.TempDir(), "outside.go")
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "outside-link.go")); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"1", filepath.Join(dir, "missing*.go")}, {"1", filepath.Join(dir, "src/*.go"), "--role", "bogus"}, {"1", outside}, {"1", filepath.Join(dir, "outside-link.go")}, {"1", filepath.Join(dir, "src/*.go"), "--depends-on", "2"}} {
		if _, err := executeGitHubTest(githubFilesCommand(linkCmd), append(args, "--json")...); err == nil {
			t.Fatal("invalid link accepted", args)
		}
		after, _ := os.ReadFile(os.Getenv("TD_FILE_STATE"))
		if string(before) != string(after) {
			t.Fatal("invalid input caused write")
		}
	}
	t.Setenv("TD_FILE_FAIL_COMMENT", "1")
	out, err := executeGitHubTest(githubFilesCommand(linkCmd), "1", filepath.Join(dir, "src/one.go"), "--json")
	if err == nil || !strings.Contains(err.Error(), "command group") || !strings.Contains(err.Error(), "earlier effects may remain") {
		t.Fatal(out, err)
	}
	if out = execute(filesCmd, "1", "--changed"); strings.Contains(out, `"status":"modified"`) {
		t.Fatal("saved refresh lost", out)
	}
	t.Setenv("TD_FILE_FAIL_COMMENT", "")
	t.Setenv("TD_FILE_FAIL_WRITE", "1")
	if _, err := executeGitHubTest(githubFilesCommand(unlinkCmd), "1", "src/*", "--json"); err == nil {
		t.Fatal("write failure swallowed")
	}
	out = execute(filesCmd, "1")
	if !strings.Contains(out, "src/one.go") {
		t.Fatal("failed removal lost association", out)
	}
	t.Setenv("TD_FILE_FAIL_WRITE", "")
	execute(unlinkCmd, "1", "src/*")
	execute(unlinkCmd, "1", "src/nested/*")
	if out = execute(filesCmd, "1"); strings.TrimSpace(out) != "[]" {
		t.Fatal(out)
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := executeGitHubTest(githubFilesCommand(filesCmd), "1", "--json"); err == nil || !strings.Contains(err.Error(), "gh CLI") {
		t.Fatal("missing gh not explicit", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("SQLite created")
	}
}

func TestGitHubFilesUnlinkedRenameUsesDestinationWithSpaces(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init")
	oldPath := filepath.Join(dir, "old name.go")
	if err := os.WriteFile(oldPath, []byte("tracked\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "old name.go")
	runGit(t, dir, "-c", "user.name=Test Fixture", "-c", "user.email=fixture@example.test", "commit", "-m", "Synthetic file fixture")
	runGit(t, dir, "mv", "old name.go", "new name.go")
	c := githubFilesCommand(filesCmd)
	c.RunE = func(cmd *cobra.Command, _ []string) error {
		return listGitHubFiles(cmd, &ghstore.Record{Issue: models.Issue{ID: "gh-1"}, Details: &ghstore.IssueDetails{Files: []models.IssueFile{{IssueID: "gh-1", FilePath: "old name.go", Role: models.FileRoleImplementation, LinkedSHA: "old"}}}}, dir)
	}
	out, err := executeGitHubTest(c, "gh-1", "--untracked", "--json")
	if err != nil {
		t.Fatal(out, err)
	}
	var rows []gitHubFileStatus
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].FilePath != "old name.go" || rows[0].Status != "deleted" || rows[1].FilePath != "new name.go" || !rows[1].Unlinked {
		t.Fatal(rows)
	}
}

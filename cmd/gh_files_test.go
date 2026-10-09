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
f=pathlib.Path(os.environ['TD_FILE_STATE']);s=json.loads(f.read_text()) if f.exists() else {'issue':{'number':1,'title':'Fixture','state':'open','body':'','labels':[{'name':'td:open'}]},'comments':[]}
if path=='repos/owner/repo':data={'full_name':'owner/repo','has_issues':True}
elif '/labels?' in path:data=[[{'name':'td:'+v} for v in ['open','in_progress','blocked','in_review','closed']]]
elif path.endswith('/comments') and method=='POST':
 if os.environ.get('TD_FILE_FAIL_COMMENT'):sys.stderr.write('HTTP 403 comment denied');sys.exit(1)
 data={'id':100+len(s['comments']),'body':json.load(sys.stdin)['body'],'created_at':'2026-10-09T00:00:00Z','updated_at':'2026-10-09T00:00:00Z','user':{'login':'fixture'}};s['comments'].append(data);f.write_text(json.dumps(s))
else:
 if method=='PATCH':
  if os.environ.get('TD_FILE_FAIL_WRITE'):sys.stderr.write('HTTP 403 write denied');sys.exit(1)
  patch=json.load(sys.stdin)
  if 'labels' in patch:patch['labels']=[{'name':v} for v in patch['labels']]
  s['issue'].update(patch);f.write_text(json.dumps(s))
 data=s['issue']
sys.stdout.write('HTTP/2.0 200 OK\r\nContent-Type: application/json\r\n\r\n'+json.dumps(data))
`
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
	if err == nil || !strings.Contains(err.Error(), "are saved") || !strings.Contains(err.Error(), "earlier changes remain") {
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

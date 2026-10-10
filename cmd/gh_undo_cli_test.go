package cmd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/ghcontext"
	"github.com/marcus/td/internal/ghstore"
)

func TestGitHubUndoCLIExecutesVerifiedLatestActionAndReportsFailures(t *testing.T) {
	dir := githubScheduleTestDir(t)
	t.Setenv("TD_SESSION_ID", "undo-cli-fixture")
	t.Setenv("TD_GH_CONTEXT_ROOT", t.TempDir())
	t.Setenv("TD_GH_CACHE", "off")
	t.Setenv("TD_GH_SYNC_HELPER", "1")
	t.Setenv("TD_ANALYTICS", "false")
	scope, err := ghcontext.Resolve(context.Background(), dir, "owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	state, err := scope.Update(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "remote.json")
	t.Setenv("TD_UNDO_REMOTE", file)
	ghPath := filepath.Join(strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0], "gh")
	script := `#!/usr/bin/env python3
import sys,os,json,pathlib,datetime
args=sys.argv[1:]
if args[0]=='auth':print('fixture-only-token');sys.exit(0)
path=next(a for a in args if a.startswith('repos/owner/repo') or a=='user')
method=args[args.index('--method')+1] if '--method' in args else 'GET'
file=pathlib.Path(os.environ['TD_UNDO_REMOTE']);remote=json.loads(file.read_text())
mode=remote.get('mode','')
def fail(text):sys.stderr.write(text);sys.exit(1)
def tick(value):return (datetime.datetime.fromisoformat(value.replace('Z','+00:00'))+datetime.timedelta(seconds=1)).isoformat().replace('+00:00','Z')
if path=='user':data={'login':'other' if mode=='actor' else 'fixture-author'}
elif path=='repos/owner/repo':data={'full_name':'owner/repo','has_issues':True}
elif path=='repos/owner/repo/issues?state=all&per_page=100':data=[[remote['target'],remote['carrier']]]
elif path=='repos/owner/repo/issues/comments?per_page=100':data=[[remote['comment']]]
elif path=='repos/owner/repo/issues/99':data=remote['carrier']
elif path=='repos/owner/repo/issues/comments/101':
 if method=='PATCH':
  remote['receipts']+=1
  if mode=='receipt' and remote['receipts']==2:fail('HTTP 403 undone receipt denied')
  remote['comment'].update(json.load(sys.stdin));remote['comment']['updated_at']=tick(remote['comment']['updated_at']);file.write_text(json.dumps(remote))
 data=remote['comment']
elif path=='repos/owner/repo/issues/1':
 if method=='PATCH':
  remote['writes']+=1
  if mode=='denied':file.write_text(json.dumps(remote));fail('HTTP 403 undo target denied')
  patch=json.load(sys.stdin)
  patch['labels']=[{'name':label} for label in patch.get('labels',[])]
  remote['target'].update(patch);remote['target']['updated_at']=tick(remote['target']['updated_at']);file.write_text(json.dumps(remote))
  if mode=='uncertain':fail('connection lost after undo PATCH')
 data=remote['target']
else:fail('unexpected endpoint '+path)
print('HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n'+json.dumps(data))
`
	if err := os.WriteFile(ghPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	makeState := func(title string) *ghstore.ActionIssueState {
		s := &ghstore.ActionIssueState{Title: title, Body: title, State: "open", Labels: []string{}, UpdatedAt: clock}
		data, e := json.Marshal(s)
		if e != nil {
			t.Fatal(e)
		}
		h := sha256.Sum256(data)
		s.Revision = hex.EncodeToString(h[:])
		return s
	}
	before, after := makeState("Before"), makeState("After")
	writeFixture := func(mode string) {
		a := ghstore.ActionJournal{OperationID: "td-op-original", SessionID: state.Session.ID, IssueID: "gh-1", CarrierID: "gh-99", Action: "update", Phase: "applied", Before: before, After: after}
		raw, e := json.Marshal(a)
		if e != nil {
			t.Fatal(e)
		}
		body := fmt.Sprintf("td action td-op-original: update gh-1 (applied)\n\n<!-- td:action:v1\n%s\n-->", raw)
		target := map[string]any{"number": 1, "title": "After", "body": "After", "state": "open", "labels": []any{}, "updated_at": clock.Format(time.RFC3339)}
		if mode == "conflict" {
			target["title"] = "Later edit"
		}
		carrier := map[string]any{"number": 99, "title": "Journal", "state": "open", "body": "<!-- td:issue:v1\n{\"entity_kind\":\"journal\",\"type\":\"task\",\"priority\":\"P2\",\"points\":0}\n-->"}
		comment := map[string]any{"id": 101, "body": body, "issue_url": "https://api.github.com/repos/owner/repo/issues/99", "user": map[string]string{"login": "fixture-author"}, "created_at": clock.Format(time.RFC3339), "updated_at": clock.Format(time.RFC3339)}
		data, e := json.Marshal(map[string]any{"target": target, "carrier": carrier, "comment": comment, "mode": mode, "writes": 0, "receipts": 0})
		if e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(file, data, 0600); e != nil {
			t.Fatal(e)
		}
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	run := func() (string, string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		child := exec.CommandContext(ctx, executable, "-test.run=^TestGitHubSyncCLIHelperProcess$", "--", "--work-dir", dir, "undo", "--json")
		child.Dir = dir
		var stdout, stderr bytes.Buffer
		child.Stdout = &stdout
		child.Stderr = &stderr
		e := child.Run()
		return stdout.String(), stderr.String(), e
	}
	read := func() map[string]any {
		data, e := os.ReadFile(file)
		if e != nil {
			t.Fatal(e)
		}
		var remote map[string]any
		if e := json.Unmarshal(data, &remote); e != nil {
			t.Fatal(e)
		}
		return remote
	}
	for _, enabled := range []string{"false", "true"} {
		t.Setenv("TD_FEATURE_SYNC_CLI", enabled)
		writeFixture("")
		out, stderr, e := run()
		if e != nil || !json.Valid([]byte(out)) || !strings.Contains(out, `"action":"undone"`) || !strings.Contains(stderr, "History scope") {
			t.Fatal(out, stderr, e)
		}
		remote := read()
		if remote["writes"] != float64(1) || remote["target"].(map[string]any)["body"] != "Before" {
			t.Fatal(remote)
		}
		out, stderr, e = run()
		if e != nil || !strings.Contains(out, `"action":"no_tracked_actions"`) || read()["writes"] != float64(1) {
			t.Fatal("undone action replayed", out, stderr, e)
		}
		for _, tc := range []struct {
			mode, fragment string
			writes         float64
		}{{"actor", "attributed", 0}, {"conflict", `"code":"conflict"`, 0}, {"denied", "HTTP 403 undo target denied", 1}, {"uncertain", "connection lost", 1}, {"receipt", "receipt is unverified", 1}} {
			writeFixture(tc.mode)
			out, stderr, e = run()
			if e == nil || !json.Valid([]byte(out)) || !strings.Contains(out, tc.fragment) || read()["writes"] != tc.writes {
				t.Fatal(tc, out, stderr, e, read())
			}
		}
	}
	if _, e := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(e) {
		t.Fatal("undo fell back to SQLite", e)
	}
}

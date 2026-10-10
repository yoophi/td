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

func TestGitHubLastCLIHistoryScopePhasesLimitsAndBulkReads(t *testing.T) {
	dir := githubScheduleTestDir(t)
	t.Setenv("TD_SESSION_ID", "history-fixture")
	t.Setenv("TD_GH_CONTEXT_ROOT", t.TempDir())
	t.Setenv("TD_GH_CACHE", "off")
	t.Setenv("TD_GH_SYNC_HELPER", "1")
	t.Setenv("TD_ANALYTICS", "false")
	t.Setenv("TD_FEATURE_SYNC_AUTOSYNC", "true")
	scope, err := ghcontext.Resolve(context.Background(), dir, "owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	state, err := scope.Update(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	dataFile := filepath.Join(t.TempDir(), "data.json")
	callsFile := filepath.Join(t.TempDir(), "calls.jsonl")
	t.Setenv("TD_LAST_DATA", dataFile)
	t.Setenv("TD_LAST_CALLS", callsFile)
	ghPath := filepath.Join(strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0], "gh")
	script := `#!/usr/bin/env python3
import sys,os,json,pathlib
args=sys.argv[1:]
with open(os.environ['TD_LAST_CALLS'],'a') as log:log.write(json.dumps(args)+'\n')
if args[0]=='auth':print('fixture-only-token');sys.exit(0)
path=next(a for a in args if a.startswith('repos/owner/repo'))
method=args[args.index('--method')+1] if '--method' in args else 'GET'
if method!='GET':sys.stderr.write('unexpected write');sys.exit(9)
source=json.loads(pathlib.Path(os.environ['TD_LAST_DATA']).read_text())
if path=='repos/owner/repo':data={'full_name':'owner/repo','has_issues':True}
elif path=='repos/owner/repo/issues?state=all&per_page=100':data=[source['issues']]
elif path=='repos/owner/repo/issues/comments?per_page=100':
 if source.get('failure'):sys.stderr.write('HTTP 403 fixture comments denied');sys.exit(1)
 data=[source['comments']]
else:sys.stderr.write('unexpected per-issue query '+path);sys.exit(9)
print('HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n'+json.dumps(data))
`
	if err := os.WriteFile(ghPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	fixtureState := func(title string) *ghstore.ActionIssueState {
		s := &ghstore.ActionIssueState{Title: title, Body: title, State: "open", Labels: []string{}, UpdatedAt: clock}
		data, e := json.Marshal(s)
		if e != nil {
			t.Fatal(e)
		}
		h := sha256.Sum256(data)
		s.Revision = hex.EncodeToString(h[:])
		return s
	}
	before, after := fixtureState("Before"), fixtureState("After")
	comments := []map[string]any{}
	for i, phase := range []string{"applied", "undone", "prepared", "applied", "applied"} {
		a := ghstore.ActionJournal{OperationID: fmt.Sprintf("td-op-%d", i+1), SessionID: state.Session.ID, IssueID: "gh-1", CarrierID: "gh-99", Action: "update", Phase: phase, Before: before, After: after}
		if phase == "prepared" {
			a.After = nil
		}
		if phase == "undone" {
			a.UndoOperationID = "td-op-undo"
			a.Restored = before
		}
		if i == 3 {
			a.Action = "create"
			a.Before = nil
		}
		if i == 4 {
			a.SessionID = "ses_other"
		}
		raw, e := json.Marshal(a)
		if e != nil {
			t.Fatal(e)
		}
		body := fmt.Sprintf("td action %s: %s %s (%s)\n\n<!-- td:action:v1\n%s\n-->", a.OperationID, a.Action, a.IssueID, a.Phase, raw)
		stamp := clock.Add(time.Duration(i) * time.Minute).Format(time.RFC3339)
		comments = append(comments, map[string]any{"id": i + 1, "body": body, "issue_url": "https://api.github.com/repos/owner/repo/issues/99", "html_url": "https://github.com/owner/repo/issues/99#issuecomment-1", "user": map[string]string{"login": "fixture-author"}, "created_at": stamp, "updated_at": stamp})
	}
	issues := []map[string]any{{"number": 1, "title": "Target", "body": "After", "state": "open"}, {"number": 99, "title": "Journal", "state": "open", "body": "<!-- td:issue:v1\n{\"entity_kind\":\"journal\",\"type\":\"task\",\"priority\":\"P2\",\"points\":0}\n-->"}}
	pending := ghstore.ActionJournal{OperationID: "td-op-6", SessionID: state.Session.ID, CarrierID: "gh-99", Action: "create", Phase: "prepared",
		CreateInput: &ghstore.ActionCreateInput{Title: "Pending creation", Body: "<!-- td:issue:v1\n{\"operation_id\":\"td-op-6\",\"type\":\"task\",\"priority\":\"P2\",\"points\":0}\n-->", Labels: []string{}}}
	raw, err := json.Marshal(pending)
	if err != nil {
		t.Fatal(err)
	}
	stamp := clock.Add(5 * time.Minute).Format(time.RFC3339)
	comments = append(comments, map[string]any{"id": 6, "body": fmt.Sprintf("td action td-op-6: create new issue (prepared)\n\n<!-- td:action:v1\n%s\n-->", raw),
		"issue_url": "https://api.github.com/repos/owner/repo/issues/99", "html_url": "https://github.com/owner/repo/issues/99#issuecomment-6", "user": map[string]string{"login": "fixture-author"}, "created_at": stamp, "updated_at": stamp})
	writeFixture := func(values []map[string]any, failure bool) {
		t.Helper()
		if values == nil {
			values = []map[string]any{}
		}
		data, e := json.Marshal(map[string]any{"issues": issues, "comments": values, "failure": failure})
		if e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(dataFile, data, 0600); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(callsFile, nil, 0600); e != nil {
			t.Fatal(e)
		}
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, string, error) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		argv := append([]string{"-test.run=^TestGitHubSyncCLIHelperProcess$", "--", "--work-dir", dir, "--refresh"}, args...)
		child := exec.CommandContext(ctx, executable, argv...)
		child.Dir = dir
		var stdout, stderr bytes.Buffer
		child.Stdout = &stdout
		child.Stderr = &stderr
		e := child.Run()
		return stdout.String(), stderr.String(), e
	}
	assertBulk := func() {
		t.Helper()
		data, e := os.ReadFile(callsFile)
		if e != nil {
			t.Fatal(e)
		}
		issues, comments := 0, 0
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			var args []string
			if e := json.Unmarshal([]byte(line), &args); e != nil {
				t.Fatal(e)
			}
			if args[0] == "auth" {
				continue
			}
			if strings.Contains(line, `"POST"`) || strings.Contains(line, `"PATCH"`) {
				t.Fatal("read wrote GitHub", line)
			}
			path := ""
			for _, arg := range args {
				if strings.HasPrefix(arg, "repos/owner/repo") {
					path = arg
					break
				}
			}
			switch path {
			case "repos/owner/repo":
			case "repos/owner/repo/issues?state=all&per_page=100":
				issues++
			case "repos/owner/repo/issues/comments?per_page=100":
				comments++
			default:
				t.Fatal("per-issue read", line)
			}
		}
		if issues != 1 || comments != 1 {
			t.Fatal("unexpected collection count", issues, comments, string(data))
		}
	}
	for _, enabled := range []string{"false", "true"} {
		t.Setenv("TD_FEATURE_SYNC_CLI", enabled)
		for _, tc := range []struct {
			args  []string
			count int
		}{{[]string{"last", "--json"}, 1}, {[]string{"last", "-n", "2", "--json"}, 2}, {[]string{"last", "--n", "99", "--json"}, 5}, {[]string{"undo", "--list", "--json"}, 5}} {
			writeFixture(comments, false)
			out, stderr, e := run(tc.args...)
			var rows []githubActionHistoryRow
			if e != nil || json.Unmarshal([]byte(out), &rows) != nil || len(rows) != tc.count || rows[0].ID != "ghc-6" || rows[0].CreateInput == nil || rows[0].CreateInput.Title != "Pending creation" || rows[0].EntityID != "" || !strings.Contains(stderr, "pre-journal/native") {
				t.Fatal(tc.args, rows, e, out, stderr)
			}
			for _, row := range rows {
				if row.SessionID != state.Session.ID || row.DataFormat != "github-native-issue-state-v1" || row.HistoryScope != "td-action-journal" {
					t.Fatal("history identity/scope lost", row)
				}
			}
			if tc.count == 5 && (!rows[3].Undone || rows[2].Phase != "prepared" || rows[2].NewData != "") {
				t.Fatal("unverified/undone phase misreported", rows)
			}
			assertBulk()
		}
		writeFixture(comments, false)
		out, stderr, e := run("last", "--n", "99")
		if e != nil || !strings.Contains(out, "[prepared]") || !strings.Contains(out, "[undone]") || !strings.Contains(out, "new issue") || !strings.Contains(stderr, "History scope") {
			t.Fatal(out, stderr, e)
		}
		assertBulk()
		writeFixture(nil, false)
		out, stderr, e = run("last", "--json")
		if e != nil || strings.TrimSpace(out) != "[]" || !strings.Contains(stderr, "pre-journal/native") {
			t.Fatal("empty result scope", out, stderr, e)
		}
		assertBulk()
		for _, tc := range []struct {
			args     []string
			fragment string
		}{{[]string{"last", "--n", "0", "--json"}, "at least 1"}, {[]string{"last", "unused", "--json"}, "unknown command"}} {
			writeFixture(comments, false)
			out, stderr, e := run(tc.args...)
			if e == nil || !json.Valid([]byte(out)) || !strings.Contains(out, tc.fragment) {
				t.Fatal(tc.args, out, stderr, e)
			}
			calls, _ := os.ReadFile(callsFile)
			if len(calls) != 0 {
				t.Fatal("invalid/unimplemented command called gh", string(calls))
			}
		}
		writeFixture(comments, false)
		out, stderr, e = run("undo", "--json")
		if e == nil || !json.Valid([]byte(out)) || !strings.Contains(out, `"code":"conflict"`) || !strings.Contains(out, "unverified outcome") {
			t.Fatal("prepared action skipped", out, stderr, e)
		}
		assertBulk()
		// A v2 comment receipt retains its native payload in last/undo --list.
		commentBody := fmt.Sprintf("Recorded comment\n\n<!-- td:activity:v1\n{\"kind\":\"comment\",\"operation_id\":\"td-op-comment\",\"session_id\":%q,\"message\":\"Recorded comment\"}\n-->", state.Session.ID)
		commentAfterIssue := *before
		commentAfterIssue.UpdatedAt = clock.Add(time.Second)
		commentAfterIssue.Revision = ""
		encoded, e := json.Marshal(commentAfterIssue)
		if e != nil {
			t.Fatal(e)
		}
		digest := sha256.Sum256(encoded)
		commentAfterIssue.Revision = hex.EncodeToString(digest[:])
		nativeComment := &ghstore.ActionCommentState{ID: "ghc-42", IssueID: "gh-1", Body: commentBody, Author: "fixture-author", CreatedAt: clock, UpdatedAt: clock}
		encoded, e = json.Marshal(nativeComment)
		if e != nil {
			t.Fatal(e)
		}
		digest = sha256.Sum256(encoded)
		nativeComment.Revision = hex.EncodeToString(digest[:])
		action := ghstore.ActionJournal{OperationID: "td-op-comment", SessionID: state.Session.ID, IssueID: "gh-1", CarrierID: "gh-99", Action: "comment_create", Phase: "applied", Before: before, After: &commentAfterIssue, CommentInput: &ghstore.ActionCommentInput{Body: commentBody}, CommentAfter: nativeComment}
		encoded, e = json.Marshal(action)
		if e != nil {
			t.Fatal(e)
		}
		commentReceipt := map[string]any{"id": 7, "body": fmt.Sprintf("td action td-op-comment: comment_create gh-1 (applied)\n\n<!-- td:action:v2\n%s\n-->", encoded), "issue_url": "https://api.github.com/repos/owner/repo/issues/99", "user": map[string]string{"login": "fixture-author"}, "created_at": clock.Add(6 * time.Minute).Format(time.RFC3339), "updated_at": clock.Add(6 * time.Minute).Format(time.RFC3339)}
		for _, args := range [][]string{{"last", "--json"}, {"undo", "--list", "--json"}} {
			writeFixture(append(append([]map[string]any{}, comments...), commentReceipt), false)
			out, stderr, e = run(args...)
			var rows []githubActionHistoryRow
			if e != nil || json.Unmarshal([]byte(out), &rows) != nil || len(rows) == 0 || rows[0].EntityType != "comment" || rows[0].EntityID != "ghc-42" || rows[0].TargetIssueID != "gh-1" || rows[0].CommentInput == nil || rows[0].CommentInput.Body != commentBody || rows[0].CommentAfter == nil || rows[0].CommentAfter.Author != "fixture-author" || rows[0].CommentDataFormat != "github-native-comment-state-v1" {
				t.Fatal("comment history lost native state", rows, out, stderr, e)
			}
			assertBulk()
		}
		writeFixture(comments, true)
		out, stderr, e = run("last", "--json")
		if e == nil || !json.Valid([]byte(out)) || !strings.Contains(out, "HTTP 403 fixture comments denied") {
			t.Fatal("read failure lost raw error", out, stderr, e)
		}
	}
	if _, e := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(e) {
		t.Fatal("history fell back to SQLite", e)
	}
}

func TestGitHubHistoryDeletedCommentKeepsOriginalIdentityAndBody(t *testing.T) {
	original := &ghstore.ActionCommentState{ID: "ghc-201", IssueID: "gh-1", Body: "Deleted original\nsecond line", Author: "original-author"}
	rows, err := githubActionHistoryRows([]ghstore.ActionJournalRecord{{ActionJournal: ghstore.ActionJournal{
		IssueID: "gh-1", Action: "comment_delete", Phase: "applied", CommentBefore: original}}})
	if err != nil || len(rows) != 1 || rows[0].EntityType != "comment" || rows[0].EntityID != "ghc-201" || rows[0].TargetIssueID != "gh-1" || rows[0].CommentBefore != original || rows[0].CommentAfter != nil || rows[0].Undone {
		t.Fatal("deleted comment mislabeled or its original lost", rows, err)
	}
}

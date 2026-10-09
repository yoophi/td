package cmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/ghcontext"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/session"
	"github.com/spf13/cobra"
)

func githubWSFixture(t *testing.T) (string, ghcontext.Scope) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fixture")
	}
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
	bin := t.TempDir()
	t.Setenv("TD_WS_STATE", filepath.Join(bin, "comments.json"))
	script := `#!/usr/bin/env python3
import sys,os,json,pathlib
args=sys.argv[1:]
if args[0]=='auth':sys.exit(0)
path=next(a for a in args if a.startswith('repos/owner/repo'))
method=args[args.index('--method')+1] if '--method' in args else 'GET'
statepath=pathlib.Path(os.environ['TD_WS_STATE'])
comments=json.loads(statepath.read_text()) if statepath.exists() else {}
issues=[{'number':n,'title':'Issue '+str(n),'state':'open','body':''} for n in [1,2]]
if os.environ.get('TD_WS_CLOSED'):
 issues[0]['state']='closed'
if os.environ.get('TD_WS_DELETED'):
 issues[0]['body']='<!-- td:issue:v1\n'+json.dumps({'type':'task','priority':'P2','points':0,'details':{'status':'closed','deleted_at':'2026-10-09T00:00:00Z'}})+'\n-->'
if os.environ.get('TD_WS_HOLDER'):
 issues[0]['body']='<!-- td:issue:v1\n'+json.dumps({'type':'task','priority':'P2','points':0,'details':{'status':'in_progress','implementer_session':os.environ['TD_WS_HOLDER']}})+'\n-->'
if path=='repos/owner/repo':data={'full_name':'owner/repo','has_issues':True}
elif path.startswith('repos/owner/repo/issues?'):data=[issues]
elif '/comments' in path:
 n=path.split('/issues/')[1].split('/')[0]
 if method=='POST':
  if os.environ.get('TD_WS_FAIL')==n:sys.stderr.write('write unavailable');sys.exit(1)
  body=json.load(sys.stdin)['body']
  data={'id':100+sum(len(a) for a in comments.values()),'body':body,'created_at':'2026-10-09T00:00:00Z','updated_at':'2026-10-09T00:00:00Z','user':{'login':'fixture'}}
  comments.setdefault(n,[]).append(data);statepath.write_text(json.dumps(comments))
 else:
  data=[comments.get(n,[])]
  mutation=os.environ.get('TD_WS_MUTATE_CONTEXT')
  if mutation:
   target=pathlib.Path(mutation);current=json.loads(target.read_text());current['session']['name']='concurrent rename';temp=target.with_suffix('.pending');temp.write_text(json.dumps(current));temp.replace(target)
else:
 n=int(path.split('/issues/')[1]);data=issues[n-1]
sys.stdout.write('HTTP/2.0 200 OK\r\nContent-Type: application/json\r\n\r\n'+json.dumps(data))
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir, scope
}

func githubWSCommand(original *cobra.Command) *cobra.Command {
	cmd := doctorTestCommand(original)
	if original == wsTagCmd {
		cmd.Flags().Bool("no-start", false, "")
	}
	if original == wsLogCmd {
		for _, typ := range []string{"blocker", "decision", "hypothesis", "tried", "result"} {
			cmd.Flags().Bool(typ, false, "")
		}
		cmd.Flags().String("only", "", "")
	}
	if original == wsHandoffCmd {
		for _, name := range []string{"done", "remaining", "decision", "uncertain"} {
			cmd.Flags().StringArray(name, nil, "")
		}
		cmd.Flags().Bool("continue", false, "")
		cmd.Flags().Bool("review", false, "")
		cmd.SetIn(strings.NewReader(""))
	}
	if original == wsShowCmd {
		cmd.Flags().Bool("full", false, "")
	}
	if original == sessionCleanupCmd {
		cmd.Flags().String("older-than", "30d", "")
		cmd.Flags().Bool("force", false, "")
	}
	return cmd
}
func githubWSExecute(t *testing.T, original *cobra.Command, args ...string) string {
	t.Helper()
	out, err := executeGitHubTest(githubWSCommand(original), append(args, "--json")...)
	if err != nil || !json.Valid([]byte(out)) {
		t.Fatalf("%s %v: %s %v", original.Name(), args, out, err)
	}
	return out
}

func TestGitHubWorkSessionLogHandoffSharedHistoryAndPartialWrite(t *testing.T) {
	_, scope := githubWSFixture(t)
	githubWSExecute(t, wsStartCmd, "work")
	// Local-only log survives before the bundle has a target.
	githubWSExecute(t, wsLogCmd, "local progress")
	githubWSExecute(t, wsTagCmd, "1", "2", "--no-start")
	githubWSExecute(t, wsLogCmd, "shared decision", "--decision")
	githubWSExecute(t, wsLogCmd, "only second", "--only", "2", "--result")
	for _, typ := range []string{"blocker", "hypothesis", "tried"} {
		githubWSExecute(t, wsLogCmd, "type "+typ, "--"+typ, "--only", "1")
	}
	if _, err := executeGitHubTest(githubWSCommand(wsLogCmd), "bad type", "--blocker", "--result", "--json"); err == nil {
		t.Fatal("conflicting log types accepted")
	}
	githubWSExecute(t, wsUntagCmd, "gh-999")
	state, _ := scope.Update(context.Background(), nil)
	id := state.ActiveWorkSession
	// Partial fanout is nonzero and preserves the active bundle and successful comment.
	t.Setenv("TD_WS_FAIL", "2")
	out, err := executeGitHubTest(githubWSCommand(wsLogCmd), "partial", "--json")
	if err == nil || !strings.Contains(err.Error(), "completed comments=[gh-1]") || out != "" {
		t.Fatalf("partial failure: %s %v", out, err)
	}
	state, _ = scope.Update(context.Background(), nil)
	if state.ActiveWorkSession != id {
		t.Fatal("failure ended bundle")
	}
	t.Setenv("TD_WS_FAIL", "")
	githubWSExecute(t, wsUntagCmd, "1")
	out = githubWSExecute(t, wsShowCmd, id, "--full")
	if !strings.Contains(out, "shared decision") || !strings.Contains(out, "partial") || !strings.Contains(out, "local progress") {
		t.Fatal("lost untagged or local history", out)
	}
	out = githubWSExecute(t, wsHandoffCmd, "--continue")
	if !strings.Contains(out, `"ended":false`) {
		t.Fatal(out)
	}
	current := githubWSExecute(t, wsCurrentCmd)
	if !strings.Contains(current, "local progress") || !strings.Contains(current, "shared decision") {
		t.Fatal(current)
	}
	githubWSExecute(t, wsHandoffCmd, "--done", "final", "--remaining", "(gh-2) follow up", "--uncertain", "question")
	state, _ = scope.Update(context.Background(), nil)
	if state.ActiveWorkSession != "" || state.WorkSessions[0].EndedAt == nil {
		t.Fatal("handoff did not end", state)
	}
	if _, err := executeGitHubTest(githubWSCommand(wsLogCmd), "after end", "--json"); err == nil {
		t.Fatal("log without bundle accepted")
	}
}

func TestGitHubSessionCleanupPreviewForceAndClaimProtection(t *testing.T) {
	dir, scope := githubWSFixture(t)
	old := time.Now().Add(-90 * 24 * time.Hour)
	state, err := scope.Update(context.Background(), func(s *ghcontext.State) error {
		s.History = []session.Session{{ID: "old", StartedAt: old}, {ID: "held", StartedAt: old}}
		_, err := s.StartWorkSession("work", "", "", "")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	other := ghcontext.Scope{Directory: scope.Directory, Path: filepath.Join(scope.Directory, "other-check.json")}
	if _, err := other.Update(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(other.Path) })
	before, err := os.ReadFile(other.Path)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TD_WS_HOLDER", "held")
	githubWSExecute(t, sessionCleanupCmd, "--older-than", "30d")
	current, _ := scope.Update(context.Background(), nil)
	if len(current.History) != 2 {
		t.Fatal("preview deleted history")
	}
	cleaned := githubWSExecute(t, sessionCleanupCmd, "--older-than", "30d", "--force")
	var cleanup struct {
		Sessions []struct {
			Session string `json:"session"`
		} `json:"sessions"`
		Held []struct {
			Session string `json:"session"`
			Claims  int    `json:"claims"`
		} `json:"held"`
	}
	if err := json.Unmarshal([]byte(cleaned), &cleanup); err != nil || len(cleanup.Sessions) != 1 || cleanup.Sessions[0].Session != "old" || len(cleanup.Held) != 1 || cleanup.Held[0].Session != "held" || cleanup.Held[0].Claims != 1 {
		t.Fatal("cleanup JSON contract", cleaned, err)
	}
	current, _ = scope.Update(context.Background(), nil)
	if len(current.History) != 1 || current.History[0].ID != "held" || current.Session.ID != state.Session.ID || current.ActiveWorkSession != state.ActiveWorkSession || len(current.WorkSessions) != 1 {
		t.Fatal("cleanup scope", current)
	}
	after, _ := os.ReadFile(other.Path)
	if string(before) != string(after) {
		t.Fatal("other context modified")
	}
	// The planning boundary must keep a historical holder even when it is old.
	candidates, kept := githubSessionCleanupPlan(state, map[string]int{"held": 2}, time.Now(), 24*time.Hour)
	if len(candidates) != 1 || candidates[0].ID != "old" || len(kept) != 1 || kept[0].Claims != 2 {
		t.Fatal(candidates, kept)
	}
	for _, age := range []string{"-1h", "0s", "invalid"} {
		if _, err := executeGitHubTest(githubWSCommand(sessionCleanupCmd), "--older-than", age, "--force", "--json"); err == nil {
			t.Fatal("invalid age accepted", age)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("SQLite created")
	}
}

func TestGitHubWorkContextRejectsConcurrentLocalChanges(t *testing.T) {
	_, scope := githubWSFixture(t)
	state, err := scope.Update(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TD_WS_MUTATE_CONTEXT", scope.Path)
	cmd := doctorTestCommand(usageCmd)
	cmd.Flags().Bool("new-session", false, "")
	out, err := executeGitHubTest(cmd, "--new-session", "--json")
	if err == nil || !strings.Contains(err.Error(), "local session or focus changed") || out != "" {
		t.Fatalf("concurrent mutation: %s %v", out, err)
	}
	current, err := scope.Update(context.Background(), nil)
	if err != nil || current.Session.ID != state.Session.ID || current.Session.Name != "concurrent rename" {
		t.Fatal("lost concurrent local changes", current, err)
	}
}

func TestGitHubWorkContextClosedAndDeletedFocus(t *testing.T) {
	_, scope := githubWSFixture(t)
	t.Setenv("TD_WS_CLOSED", "yes")
	out, err := executeGitHubTest(doctorTestCommand(resumeCmd), "gh-1", "--json")
	var resumed struct {
		Status string `json:"status"`
		ID     string `json:"id"`
	}
	if err != nil || json.Unmarshal([]byte(out), &resumed) != nil || resumed.Status != "closed" || resumed.ID != "gh-1" {
		t.Fatalf("closed focus: %s %v", out, err)
	}
	t.Setenv("TD_WS_DELETED", "yes")
	out, err = executeGitHubTest(doctorTestCommand(statusCmd), "--json")
	var status struct {
		SavedFocus   string `json:"saved_focus"`
		FocusMissing bool   `json:"focus_missing"`
	}
	if err != nil || json.Unmarshal([]byte(out), &status) != nil || !status.FocusMissing || status.SavedFocus != "gh-1" {
		t.Fatalf("deleted focus: %s %v", out, err)
	}
	state, err := scope.Update(context.Background(), nil)
	if err != nil || state.Focus != "gh-1" {
		t.Fatal("saved focus lost", state, err)
	}
}

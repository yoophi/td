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
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/serve"
	"github.com/marcus/td/internal/session"
	"github.com/marcus/td/pkg/monitor"
)

func TestGitHubWorkContextTouchedDeletedHistory(t *testing.T) {
	now := time.Now()
	s := &serve.GitHubContextSnapshot{Data: &monitor.RefreshDataMsg{}, Records: []ghstore.Record{{Issue: models.Issue{ID: "gh-1", CreatorSession: "actor", DeletedAt: &now}}, {Issue: models.Issue{ID: "gh-2"}}, {Issue: models.Issue{ID: "gh-3", Status: models.StatusInProgress, ImplementerSession: "actor"}}}, Activities: map[string][]models.Activity{"gh-1": {{Kind: "handoff", SessionID: "actor", Done: []string{"saved"}}}, "gh-2": {{Kind: "log", SessionID: "actor"}}}}
	p := githubWorkContextPayload(&ghcontext.State{Session: session.Session{ID: "actor"}, Focus: "gh-1"}, s)
	if len(p["issues_touched"].([]string)) != 3 || len(p["in_progress"].([]models.Issue)) != 1 || !p["focus_missing"].(bool) || !p["work_sessions_supported"].(bool) {
		t.Fatal(p)
	}
	if len(p["history"].(map[string][]models.Activity)["gh-1"]) != 1 {
		t.Fatal("deleted issue history lost")
	}
}

func TestGitHubWorkContextCLIQueuesAndPreservedFocusOnReadFailure(t *testing.T) {
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
	state, err := scope.Update(context.Background(), func(s *ghcontext.State) error { s.Focus = "gh-99"; return nil })
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	script := `#!/bin/sh
if [ "$1" = auth ]; then exit 0; fi
printf 'HTTP/2.0 200 OK\r\nContent-Type: application/json\r\n\r\n'
if [ "$4" = repos/owner/repo ]; then printf '{"full_name":"owner/repo","has_issues":true}';exit 0; fi
case "$6" in
 'repos/owner/repo/issues?state=all&per_page=100') printf '[[{"number":1,"title":"First","state":"open","body":""}]]' ;;
 'repos/owner/repo/issues/1') printf '{"number":1,"title":"First","state":"open","body":""}' ;;
 'repos/owner/repo/issues/comments?per_page=100')
  if [ "$TD_CONTEXT_FAIL" = yes ]; then echo 'history unavailable' >&2;exit 1;fi
  printf '[[]]' ;;
 *) echo 'unexpected request' >&2;exit 3 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	cmd := doctorTestCommand(statusCmd)
	out, err := executeGitHubTest(cmd, "--json")
	var p map[string]json.RawMessage
	if err != nil || json.Unmarshal([]byte(out), &p) != nil || string(p["focus_missing"]) != "true" || string(p["review_workflows_supported"]) != "true" || !strings.Contains(string(p["ready"]), "gh-1") {
		t.Fatalf("%s %v", out, err)
	}
	t.Setenv("TD_CONTEXT_FAIL", "yes")
	cmd = doctorTestCommand(usageCmd)
	cmd.Flags().Bool("new-session", false, "")
	if out, err = executeGitHubTest(cmd, "--json", "--new-session"); err == nil || out != "" {
		t.Fatalf("partial context %s %v", out, err)
	}
	current, err := scope.Update(context.Background(), nil)
	if err != nil || current.Session.ID != state.Session.ID || current.Focus != "gh-99" {
		t.Fatal(current, err)
	}
	if out, err = executeGitHubTest(doctorTestCommand(resumeCmd), "gh-1", "--json"); err == nil || out != "" {
		t.Fatalf("failed resume %s %v", out, err)
	}
	current, _ = scope.Update(context.Background(), nil)
	if current.Focus != "gh-99" {
		t.Fatal("failed read changed focus")
	}
	t.Setenv("TD_CONTEXT_FAIL", "no")
	if out, err = executeGitHubTest(doctorTestCommand(resumeCmd), "gh-1", "--json"); err != nil || !strings.Contains(out, `"id":"gh-1"`) {
		t.Fatalf("resume %s %v", out, err)
	}
	if _, err = os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("SQLite DB created")
	}
}

func TestGitHubWorkContextActiveBundleAndDeletedTag(t *testing.T) {
	now := time.Now()
	state := &ghcontext.State{Session: session.Session{ID: "actor"}, ActiveWorkSession: "ws-1", WorkSessions: []ghcontext.WorkSession{{WorkSession: models.WorkSession{ID: "ws-1", SessionID: "actor"}, Issues: []string{"gh-1", "gh-2", "gh-99"}, LocalActivities: []models.Activity{{Kind: "log", Message: "local", CreatedAt: now}}}}}
	snapshot := &serve.GitHubContextSnapshot{Data: &monitor.RefreshDataMsg{}, Records: []ghstore.Record{{Issue: models.Issue{ID: "gh-1", Status: models.StatusClosed}}, {Issue: models.Issue{ID: "gh-2", DeletedAt: &now}}}, Activities: map[string][]models.Activity{"gh-2": {{Kind: "handoff", WorkSessionID: "ws-1", Done: []string{"shared"}, CreatedAt: now}}}}
	p := githubWorkContextPayload(state, snapshot)
	if p["work_session"].(*ghcontext.WorkSession).ID != "ws-1" || len(p["work_session_activities"].([]models.Activity)) != 2 || len(p["work_session_missing_issues"].([]string)) != 2 {
		t.Fatal(p)
	}
	// Closed tagged issues remain visible; deleted/missing tags are reported.
	if p["work_session_missing_issues"].([]string)[0] != "gh-2" {
		t.Fatal(p)
	}
	state.ActiveWorkSession = ""
	p = githubWorkContextPayload(state, snapshot)
	if p["work_session"].(*ghcontext.WorkSession) != nil || len(p["work_session_activities"].([]models.Activity)) != 0 || len(p["work_session_history"].([]ghcontext.WorkSession)) != 1 {
		t.Fatal("inactive bundle/context history", p)
	}
}

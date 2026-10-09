package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marcus/td/internal/db"
	"github.com/marcus/td/internal/ghcontext"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/session"
	"github.com/spf13/cobra"
)

func handoffCheckCommand() *cobra.Command {
	cmd := doctorTestCommand(checkHandoffCmd)
	cmd.Flags().Bool("quiet", false, "")
	return cmd
}

func TestGitHubCheckHandoffExitJSONQuietAndOwnership(t *testing.T) {
	dir, scope := githubWSFixture(t)
	state, err := scope.Update(context.Background(), func(s *ghcontext.State) error { s.Focus = "gh-1"; return nil })
	if err != nil {
		t.Fatal(err)
	}
	// Focus alone is not a claim; another actor's claim must not count.
	t.Setenv("TD_WS_HOLDER", "another-actor")
	out, err := executeGitHubTest(handoffCheckCommand(), "--json")
	var result struct {
		Needed bool     `json:"needs_handoff"`
		IDs    []string `json:"in_progress_issues"`
		Focus  string   `json:"focused_issue"`
		Count  int      `json:"in_progress_count"`
		WS     string   `json:"active_work_session"`
	}
	if err != nil || json.Unmarshal([]byte(out), &result) != nil || result.Needed || result.IDs == nil || len(result.IDs) != 0 || result.Focus != "gh-1" {
		t.Fatalf("empty check: %s %v", out, err)
	}
	t.Setenv("TD_WS_HOLDER", state.Session.ID)
	out, err = executeGitHubTest(handoffCheckCommand(), "--json")
	if !errors.Is(err, errSilentExit) || json.Unmarshal([]byte(out), &result) != nil || !result.Needed || result.Count != 1 || len(result.IDs) != 1 || result.IDs[0] != "gh-1" {
		t.Fatalf("needed JSON: %s %v", out, err)
	}
	out, err = executeGitHubTest(handoffCheckCommand(), "--quiet")
	if !errors.Is(err, errSilentExit) || out != "" {
		t.Fatalf("quiet: %s %v", out, err)
	}
	// Active bundle alone also requires a handoff.
	t.Setenv("TD_WS_HOLDER", "")
	_, err = scope.Update(context.Background(), func(s *ghcontext.State) error { _, err := s.StartWorkSession("work", "", "", ""); return err })
	if err != nil {
		t.Fatal(err)
	}
	out, err = executeGitHubTest(handoffCheckCommand(), "--json")
	if !errors.Is(err, errSilentExit) || json.Unmarshal([]byte(out), &result) != nil || !result.Needed || result.Count != 0 || result.WS == "" {
		t.Fatalf("bundle: %s %v", out, err)
	}
	// An unavailable gh executable must be an error, even in quiet mode.
	t.Setenv("PATH", t.TempDir())
	out, err = executeGitHubTest(handoffCheckCommand(), "--json")
	if err == nil || errors.Is(err, errSilentExit) || out != "" {
		t.Fatalf("unavailable remote hidden: %s %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("SQLite created")
	}
}

func TestGitHubOrdinaryActivityTracksActiveBundleWithoutTag(t *testing.T) {
	_, scope := githubWSFixture(t)
	githubWSExecute(t, wsStartCmd, "work")
	cmd := doctorTestCommand(logCmd)
	cmd.Flags().String("issue", "", "")
	out, err := executeGitHubTest(cmd, "ordinary progress", "--issue", "2", "--json")
	if err != nil || !json.Valid([]byte(out)) {
		t.Fatal(out, err)
	}
	state, err := scope.Update(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ws, err := state.CurrentWorkSession()
	if err != nil || len(ws.HistoryIssues) != 1 || ws.HistoryIssues[0] != "gh-2" || len(ws.Issues) != 0 {
		t.Fatal("missing bundle association", ws, err)
	}
	current := githubWSExecute(t, wsCurrentCmd)
	if !strings.Contains(current, "ordinary progress") {
		t.Fatal("ordinary log missing from bundle history", current)
	}
}

func TestSQLiteCheckHandoffJSONAndQuietExit(t *testing.T) {
	saveAndRestoreGlobals(t)
	dir := t.TempDir()
	baseDirOverride = &dir
	database, err := db.Initialize(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	sess, err := session.GetOrCreate(database)
	if err != nil {
		t.Fatal(err)
	}
	cmd := handoffCheckCommand()
	if err := cmd.Flags().Set("json", "true"); err != nil {
		t.Fatal(err)
	}
	setJSONFlag(t, true)
	var runErr error
	out := captureStdout(t, func() { runErr = cmd.RunE(cmd, nil) })
	if runErr != nil || !strings.Contains(out, `"needs_handoff": false`) {
		t.Fatal(out, runErr)
	}
	issue := &models.Issue{Title: "in progress", Status: models.StatusInProgress, ImplementerSession: sess.ID}
	if err := database.CreateIssue(issue); err != nil {
		t.Fatal(err)
	}
	if err := database.UpdateIssue(issue); err != nil {
		t.Fatal(err)
	}
	out = captureStdout(t, func() { runErr = cmd.RunE(cmd, nil) })
	if !errors.Is(runErr, errSilentExit) || !json.Valid([]byte(out)) || !strings.Contains(out, `"needs_handoff": true`) {
		t.Fatal(out, runErr)
	}
	setJSONFlag(t, false)
	if err := cmd.Flags().Set("json", "false"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("quiet", "true"); err != nil {
		t.Fatal(err)
	}
	out = captureStdout(t, func() { runErr = cmd.RunE(cmd, nil) })
	if !errors.Is(runErr, errSilentExit) || out != "" {
		t.Fatal("SQLite quiet", out, runErr)
	}
}

func TestGitHubActivityAssociationFailureReportsSavedComment(t *testing.T) {
	dir := t.TempDir()
	scope := ghcontext.Scope{Directory: dir, Path: filepath.Join(dir, "context.json")}
	store := &memoryActivities{}
	tracker := &gitHubActivityTracker{ActivityStore: store, Scope: scope}
	activity, err := tracker.AppendActivity(context.Background(), "gh-2", models.Activity{Kind: "log", Message: "saved", SessionID: "actor", WorkSessionID: "missing", OperationID: "known-op"})
	if err == nil || activity == nil || store.writes != 1 || !strings.Contains(err.Error(), "was saved on gh-2") || !strings.Contains(err.Error(), "known-op") {
		t.Fatal(activity, err, store.writes)
	}
}

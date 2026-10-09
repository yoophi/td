package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/db"
	"github.com/marcus/td/internal/ghcontext"
	"github.com/marcus/td/internal/models"
	"github.com/spf13/cobra"
)

func diagnosticTestCommand(original *cobra.Command) *cobra.Command {
	c := doctorTestCommand(original)
	c.Flags().Bool("clear", false, "")
	c.Flags().Bool("count", false, "")
	c.Flags().Int("limit", 0, "")
	c.Flags().String("since", "", "")
	c.Flags().String("session", "", "")
	return c
}

func TestGitHubDiagnosticIdentityFiltersJSONAndDisable(t *testing.T) {
	dir := storeTestDir(t)
	runGit(t, dir, "init")
	// No GitHub remote or gh is needed for device-local diagnostics.
	if err := config.SetStore(dir, "gh-issue", &models.GitHubStoreConfig{Remote: "origin", Repo: "owner/repo"}); err != nil {
		t.Fatal(err)
	}
	scope, err := ghcontext.Resolve(context.Background(), dir, "owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(scope.Path) })
	id := diagnosticSessionID(dir)
	if !strings.HasPrefix(id, "ses_") {
		t.Fatal(id)
	}
	logAgentError([]string{"test", "--token", "local-only"}, "fixture failure")
	errs, err := db.ReadAgentErrors(dir)
	if err != nil || len(errs) != 1 || errs[0].SessionID != id {
		t.Fatalf("%+v %v", errs, err)
	}
	out, err := executeGitHubTest(diagnosticTestCommand(statsErrorsCmd), "--json", "--session", id, "--since", "1h", "--limit", "1")
	var entry db.AgentError
	if err != nil || json.Unmarshal([]byte(out), &entry) != nil || entry.SessionID != id {
		t.Fatalf("%s %v", out, err)
	}
	out, err = executeGitHubTest(diagnosticTestCommand(errorsCmd), "--json", "--session", "other")
	if err != nil || out != "" {
		t.Fatalf("empty JSONL %q %v", out, err)
	}
	out, err = executeGitHubTest(diagnosticTestCommand(errorsCmd), "--json", "--count")
	if err != nil || strings.TrimSpace(out) != "1" {
		t.Fatalf("%q %v", out, err)
	}
	out, err = executeGitHubTest(diagnosticTestCommand(errorsCmd), "--json", "--clear")
	if err != nil || !strings.Contains(out, `"cleared":true`) {
		t.Fatalf("%q %v", out, err)
	}

	oldCmd, oldStart := executedCmd, cmdStartTime
	t.Cleanup(func() { executedCmd = oldCmd; cmdStartTime = oldStart })
	executedCmd = statsErrorsCmd
	cmdStartTime = time.Now()
	t.Setenv("TD_ANALYTICS", "true")
	logAnalytics(errors.New("fixture failed command"))
	events, err := db.ReadCommandUsage(dir)
	if err != nil || len(events) != 1 || events[0].SessionID != id || events[0].Command != "stats" || events[0].Subcommand != "errors" || events[0].Success {
		t.Fatalf("%+v %v", events, err)
	}
	t.Setenv("TD_ANALYTICS", "false")
	logAnalytics(nil)
	events, err = db.ReadCommandUsage(dir)
	if err != nil || len(events) != 1 {
		t.Fatal(events, err)
	}
	out, err = executeGitHubTest(diagnosticTestCommand(statsAnalyticsCmd), "--json", "--since", "1h", "--limit", "1")
	var summary db.AnalyticsSummary
	if err != nil || json.Unmarshal([]byte(out), &summary) != nil || summary.CommandCounts["stats errors"] != 1 || summary.SessionActivity[id] != 1 || slices.Contains(summary.NeverUsed, "stats errors") {
		t.Fatalf("%s %v", out, err)
	}
	out, err = executeGitHubTest(diagnosticTestCommand(statsAnalyticsCmd), "--json", "--clear")
	if err != nil || !strings.Contains(out, `"cleared":true`) {
		t.Fatalf("%s %v", out, err)
	}
	out, err = executeGitHubTest(diagnosticTestCommand(statsAnalyticsCmd), "--json")
	summary = db.AnalyticsSummary{}
	if err != nil || json.Unmarshal([]byte(out), &summary) != nil || summary.TotalCommands != 0 || len(summary.CommandCounts) != 0 || !slices.Contains(summary.NeverUsed, "stats analytics") {
		t.Fatalf("empty JSON %s %v", out, err)
	}
	if _, err = os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("SQLite issue DB created")
	}
	if !slices.Contains(getAllCommandNames(), "stats analytics") {
		t.Fatal("missing registered subcommands")
	}
}

func TestDiagnosticNestedCommandPath(t *testing.T) {
	storeTestDir(t)
	r := &cobra.Command{Use: "td"}
	a := &cobra.Command{Use: "task"}
	b := &cobra.Command{Use: "comments"}
	c := &cobra.Command{Use: "add"}
	r.AddCommand(a)
	a.AddCommand(b)
	b.AddCommand(c)
	e := buildCommandEvent(c, nil)
	if e.Command != "task" || e.Subcommand != "comments add" {
		t.Fatal(e)
	}
}

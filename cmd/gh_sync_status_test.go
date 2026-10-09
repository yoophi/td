package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestGitHubSyncStatusReportsDirectStoreWithoutSQLiteOrTaskFetch(t *testing.T) {
	dir := githubScheduleTestDir(t)
	calls := recordScheduleGHCalls(t)
	for _, enabled := range []string{"false", "true"} {
		t.Setenv("TD_FEATURE_SYNC_CLI", enabled)
		cmd := &cobra.Command{Use: syncStatusCmd.Use, Args: syncStatusCmd.Args, RunE: syncStatusCmd.RunE}
		cmd.Flags().Bool("json", false, "")
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"--json"})
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		var report githubSyncStatusReport
		if err := json.Unmarshal(out.Bytes(), &report); err != nil || report.Store != "gh-issue" || report.Repository != "owner/repo" || report.Mode != "direct-gh" || report.LocalQueue || report.TDSyncServerUsed || report.Cache == nil {
			t.Fatal("misleading sync diagnostic", out.String(), err)
		}
		if _, err := os.Stat(os.Getenv("TD_SCHEDULE_STATE")); !os.IsNotExist(err) {
			t.Fatal("status changed remote state", err)
		}
	}
	invocations, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	repoReads := 0
	for _, line := range strings.Split(strings.TrimSpace(string(invocations)), "\n") {
		var args []string
		if err := json.Unmarshal([]byte(line), &args); err != nil {
			t.Fatal(err)
		}
		if args[0] == "auth" {
			continue
		}
		if args[0] != "api" {
			t.Fatal("unexpected gh call", args)
		}
		for _, arg := range args {
			if strings.HasPrefix(arg, "repos/") {
				if arg != "repos/owner/repo" {
					t.Fatal("status fetched task collections", args)
				}
				repoReads++
			}
		}
	}
	if repoReads != 2 {
		t.Fatal("repository verification changed", repoReads, string(invocations))
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("status opened SQLite", err)
	}
}

func recordScheduleGHCalls(t *testing.T) string {
	t.Helper()
	calls := filepath.Join(t.TempDir(), "calls.jsonl")
	t.Setenv("TD_SYNC_STATUS_CALLS", calls)
	ghPath := filepath.Join(strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0], "gh")
	script, err := os.ReadFile(ghPath)
	if err != nil {
		t.Fatal(err)
	}
	old := "args=sys.argv[1:]\n"
	if !strings.Contains(string(script), old) {
		t.Fatal("fixture invocation parser changed")
	}
	script = []byte(strings.Replace(string(script), old, old+"with open(os.environ['TD_SYNC_STATUS_CALLS'],'a') as log: log.write(json.dumps(args)+'\\n')\n", 1))
	if err := os.WriteFile(ghPath, script, 0755); err != nil {
		t.Fatal(err)
	}
	return calls
}

func TestGitHubSyncStatusMissingGHReturnsExplicitFailure(t *testing.T) {
	dir := githubScheduleTestDir(t)
	t.Setenv("PATH", t.TempDir())
	cmd := &cobra.Command{Use: syncStatusCmd.Use, Args: syncStatusCmd.Args, RunE: syncStatusCmd.RunE, SilenceErrors: true, SilenceUsage: true}
	var out bytes.Buffer
	cmd.SetOut(&out)
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "gh CLI not found") || out.Len() != 0 {
		t.Fatal("failed diagnostic looked successful", err, out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("failed status opened SQLite", err)
	}
}

func TestGitHubSyncStatusSQLiteRouteDoesNotOpenGH(t *testing.T) {
	storeTestDir(t)
	t.Setenv("PATH", t.TempDir())
	if handled, err := githubSyncStatus(&cobra.Command{}); handled || err != nil {
		t.Fatal("SQLite route changed", handled, err)
	}
}

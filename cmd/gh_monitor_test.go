package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func TestGitHubMonitorMissingGHFailsBeforeSQLiteOrUI(t *testing.T) {
	dir := githubScheduleTestDir(t)
	t.Setenv("PATH", t.TempDir())
	cmd := &cobra.Command{Use: "monitor", RunE: monitorCmd.RunE, SilenceErrors: true, SilenceUsage: true}
	cmd.Flags().Duration("interval", time.Minute, "")
	_, err := executeGitHubTest(cmd)
	if err == nil || !strings.Contains(err.Error(), "gh CLI") {
		t.Fatal("missing gh not explicit", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("monitor fell back to SQLite", err)
	}
}

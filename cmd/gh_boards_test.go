package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestGitHubBoardInvalidDraftsBeforeNetwork(t *testing.T) {
	dir := githubScheduleTestDir(t)
	t.Setenv("PATH", t.TempDir())
	for _, tc := range []struct {
		cmd  *cobra.Command
		args []string
	}{
		{boardCreateCmd, []string{"Draft", "--query", "unknown_field = x"}},
		{boardEditCmd, []string{"bd-gh-4", "--view-mode", "invalid", "--name", "Changed"}},
		{boardEditCmd, []string{"bd-gh-4"}},
		{boardMoveCmd, []string{"bd-gh-4", "gh-1", "0"}},
		{boardShowCmd, []string{"bd-gh-4", "--status", "unknown"}},
		{boardListCmd, []string{"ignored"}},
	} {
		command := &cobra.Command{Use: tc.cmd.Use, RunE: tc.cmd.RunE, SilenceErrors: true, SilenceUsage: true}
		command.Flags().String("query", "", "")
		command.Flags().String("name", "", "")
		command.Flags().String("view-mode", "", "")
		command.Flags().StringArray("status", nil, "")
		out, err := executeGitHubTest(command, tc.args...)
		if err == nil || out != "" || strings.Contains(err.Error(), "gh CLI") {
			t.Fatal(tc.args, out, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("SQLite created", err)
	}
}

package cmd

import (
	"bytes"
	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/models"
	"github.com/spf13/cobra"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitHubImportInvalidStdinFailsBeforeGHAndSQLite(t *testing.T) {
	dir := storeTestDir(t)
	if err := config.SetStore(dir, "gh-issue", &models.GitHubStoreConfig{Repo: "owner/repo", Remote: "origin"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	for _, input := range []string{`[{"future":1}]`, `[{"issue":{"id":"td-abc","title":"Task","parent_id":"td-missing"}}]`} {
		c := &cobra.Command{Use: importCmd.Use, Args: importCmd.Args, RunE: importCmd.RunE, SilenceErrors: true, SilenceUsage: true}
		c.Flags().String("format", "json", "")
		c.Flags().Bool("dry-run", false, "")
		c.Flags().Bool("force", false, "")
		c.Flags().Bool("json", false, "")
		c.SetIn(strings.NewReader(input))
		var out bytes.Buffer
		c.SetOut(&out)
		c.SetArgs([]string{"-", "--dry-run"})
		err := c.Execute()
		if err == nil || strings.Contains(err.Error(), "gh CLI not found") || out.Len() != 0 {
			t.Fatal("invalid input reached network or emitted partial result", err, out.String())
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("import opened SQLite")
	}
}

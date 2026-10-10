package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/models"
	"github.com/spf13/cobra"
)

func TestGitHubReconcileOptionsRejectBeforeOpeningOrWritingSQLite(t *testing.T) {
	dir := t.TempDir()
	previous := baseDirOverride
	baseDirOverride = &dir
	t.Cleanup(func() { baseDirOverride = previous })
	cfg := &models.Config{Store: "sqlite"}
	if err := config.Save(dir, cfg); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"--apply"}, {"--reconcile", "ghc-101"}, {"--reconcile", "ghc-101", "--list"}} {
		c := &cobra.Command{Use: undoCmd.Use, Args: undoCmd.Args, RunE: undoCmd.RunE, SilenceUsage: true, SilenceErrors: true}
		c.Flags().Bool("apply", false, "")
		c.Flags().Bool("list", false, "")
		c.Flags().String("reconcile", "", "")
		c.SetArgs(args)
		if err := c.Execute(); err == nil {
			t.Fatal("unsupported reconciliation options accepted", args)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("reconciliation opened SQLite", err)
	}
}

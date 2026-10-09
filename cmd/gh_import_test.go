package cmd

import (
	"bytes"
	"encoding/json"
	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/ghstore"
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
	for _, input := range []string{`[{"future":1}]`, `[{"issue":{"id":"td-abc","title":"Task","parent_id":"td-missing"}}]`, `[{"issue":{"id":"td-one","title":"One"},"dependencies":["td-two"]},{"issue":{"id":"td-two","title":"Two"},"dependencies":["td-one"]}]`} {
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

func TestGitHubImportValidDryRunSourceNamespaceAndNoWrites(t *testing.T) {
	githubScheduleTestDir(t)
	c := &cobra.Command{Use: importCmd.Use, RunE: importCmd.RunE, SilenceErrors: true, SilenceUsage: true}
	c.Flags().String("source", "", "")
	c.Flags().String("format", "json", "")
	c.Flags().Bool("dry-run", false, "")
	c.Flags().Bool("force", false, "")
	c.Flags().Bool("json", false, "")
	c.SetIn(strings.NewReader(`[{"issue":{"id":"td-one","title":"Legacy task"}}]`))
	var out bytes.Buffer
	c.SetOut(&out)
	c.SetArgs([]string{"-", "--source", "project-a", "--dry-run", "--json"})
	if err := c.Execute(); err != nil {
		t.Fatal(err)
	}
	var plan ghstore.ImportPlan
	if err := json.Unmarshal(out.Bytes(), &plan); err != nil || !plan.DryRun || len(plan.Rows) != 1 || plan.Rows[0].SourceKey != "legacy:project-a:td-one" || plan.Rows[0].Action != "create" {
		t.Fatal("valid CLI plan lost source namespace", out.String(), err)
	}
	if _, err := os.Stat(os.Getenv("TD_SCHEDULE_STATE")); !os.IsNotExist(err) {
		t.Fatal("dry-run wrote remote state", err)
	}
}

func TestGitHubImportSourceValidationBeforeGHAndSQLite(t *testing.T) {
	dir := storeTestDir(t)
	if err := config.SetStore(dir, "gh-issue", &models.GitHubStoreConfig{Repo: "owner/repo", Remote: "origin"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	c := &cobra.Command{Use: importCmd.Use, RunE: importCmd.RunE, SilenceErrors: true, SilenceUsage: true}
	c.Flags().String("source", "", "")
	c.Flags().String("format", "json", "")
	c.Flags().Bool("dry-run", false, "")
	c.SetIn(strings.NewReader(`[{"issue":{"id":"td-one","title":"Legacy task"}}]`))
	c.SetArgs([]string{"-", "--source", "invalid:namespace", "--dry-run"})
	err := c.Execute()
	if err == nil || !strings.Contains(err.Error(), "invalid import source namespace") {
		t.Fatal("source validation reached gh", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("source validation opened SQLite")
	}
}

func TestSQLiteImportRejectsGitHubSourceFlagBeforeOpeningDatabase(t *testing.T) {
	dir := storeTestDir(t)
	c := &cobra.Command{Use: importCmd.Use, RunE: importCmd.RunE, SilenceErrors: true, SilenceUsage: true}
	c.Flags().String("source", "", "")
	c.SetArgs([]string{"does-not-exist.json", "--source", "project-a"})
	err := c.Execute()
	if err == nil || !strings.Contains(err.Error(), "--source requires store=gh-issue") {
		t.Fatal("SQLite silently ignored source flag", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("invalid SQLite flag opened database")
	}
}

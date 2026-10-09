package cmd

import (
	"bytes"
	"encoding/json"
	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/db"
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

func TestGitHubImportUnnamedLegacyExecutionFailsBeforeGH(t *testing.T) {
	dir := storeTestDir(t)
	if err := config.SetStore(dir, "gh-issue", &models.GitHubStoreConfig{Repo: "owner/repo", Remote: "origin"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	for _, input := range []struct{ format, data string }{
		{"json", `[{"issue":{"id":"td-one","title":"Task"}}]`},
		{"md", "## Task\n\nDescription\n"},
	} {
		c := &cobra.Command{Use: importCmd.Use, RunE: importCmd.RunE, SilenceErrors: true, SilenceUsage: true}
		c.Flags().String("source", "", "")
		c.Flags().String("format", "json", "")
		c.Flags().Bool("dry-run", false, "")
		c.SetIn(strings.NewReader(input.data))
		var out bytes.Buffer
		c.SetOut(&out)
		c.SetArgs([]string{"-", "--format", input.format})
		err := c.Execute()
		if err == nil || !strings.Contains(err.Error(), "requires a source namespace") || out.Len() != 0 {
			t.Fatal(input.format, "unnamed source reached gh or emitted output", err, out.String())
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("unnamed import opened SQLite", err)
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

func TestGitHubImportCLIExecuteExportReimportAndNoSQLiteFallback(t *testing.T) {
	dir := githubScheduleTestDir(t)
	ghPath := filepath.Join(strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0], "gh")
	script, err := os.ReadFile(ghPath)
	if err != nil {
		t.Fatal(err)
	}
	original := "data={'id':len(state['comments'])+1,'body':body"
	replacement := "data={'issue_url':'https://api.github.com/repos/owner/repo/issues/'+path.split('/issues/')[1].split('/')[0],'id':len(state['comments'])+1,'body':body"
	if !strings.Contains(string(script), original) {
		t.Fatal("fixture comment format changed")
	}
	if err := os.WriteFile(ghPath, []byte(strings.Replace(string(script), original, replacement, 1)), 0755); err != nil {
		t.Fatal(err)
	}
	execute := func(input string, args ...string) (ghstore.ImportResult, error) {
		t.Helper()
		c := &cobra.Command{Use: importCmd.Use, RunE: importCmd.RunE, SilenceErrors: true, SilenceUsage: true}
		c.Flags().String("source", "", "")
		c.Flags().String("format", "json", "")
		c.Flags().Bool("dry-run", false, "")
		c.Flags().Bool("force", false, "")
		c.Flags().Bool("json", false, "")
		c.SetIn(strings.NewReader(input))
		var out bytes.Buffer
		c.SetOut(&out)
		c.SetArgs(append([]string{"-", "--json"}, args...))
		err := c.Execute()
		var result ghstore.ImportResult
		if err == nil {
			if err := json.Unmarshal(out.Bytes(), &result); err != nil {
				t.Fatal(out.String(), err)
			}
		}
		return result, err
	}
	input := `[{"issue":{"id":"td-parent","title":"Parent"},"logs":[{"id":"log-old","session_id":"old-worker","message":"Original progress","type":"progress","timestamp":"2026-10-01T12:00:00Z"}],"handoffs":[{"id":"handoff-old","session_id":"old-worker","done":["Original handoff"],"timestamp":"2026-10-01T12:00:00Z"}]},{"issue":{"id":"td-child","title":"Child","parent_id":"td-parent"},"dependencies":["td-parent"]}]`
	result, err := execute(input, "--source", "project-a")
	if err != nil || result.Created != 2 || result.Completed != 2 {
		t.Fatal(result, err)
	}
	before, err := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
	if err != nil {
		t.Fatal(err)
	}
	result, err = execute(input, "--source", "project-a")
	after, _ := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
	if err != nil || result.Skipped != 2 || !bytes.Equal(before, after) {
		t.Fatal("repeat changed remote fixture", result, err)
	}
	export := &cobra.Command{Use: exportCmd.Use, RunE: exportCmd.RunE, SilenceErrors: true, SilenceUsage: true}
	export.Flags().String("format", "json", "")
	export.Flags().String("output", "", "")
	export.Flags().Bool("all", false, "")
	export.Flags().Bool("render-markdown", false, "")
	var exported bytes.Buffer
	export.SetOut(&exported)
	export.SetArgs([]string{"--all"})
	if err := export.Execute(); err != nil {
		t.Fatal(err)
	}
	items, err := ghstore.ParseImportJSON(exported.Bytes())
	if err != nil {
		t.Fatal(exported.String(), err)
	}
	found := false
	for _, item := range items {
		if item.Issue.Title == "Parent" {
			found = len(item.Logs) == 1 && len(item.Handoffs) == 1 && item.Logs[0].OriginalSessionID == "old-worker"
		}
	}
	if !found {
		t.Fatal("CLI export lost original portable history")
	}
	database, err := db.Initialize(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	for _, item := range items {
		if err := database.ImportItemRaw(&item.Issue, item.Logs, item.Handoffs, item.Dependencies, item.Files, false); err != nil {
			t.Fatal("GitHub export could not restore SQLite rows", err)
		}
	}
	for _, item := range items {
		if item.Issue.Title == "Parent" {
			logs, err := database.GetLogs(item.Issue.ID, 0)
			if err != nil || len(logs) != 1 || logs[0].Message != "Original progress" || !strings.HasPrefix(logs[0].SessionID, "imported-") {
				t.Fatal("SQLite restore lost content or reused active actor", logs, err)
			}
			handoff, err := database.GetLatestHandoff(item.Issue.ID)
			if err != nil || handoff == nil || len(handoff.Done) != 1 || handoff.Done[0] != "Original handoff" {
				t.Fatal("SQLite restore lost original handoff", handoff, err)
			}
		}
	}
	result, err = execute(exported.String())
	after, _ = os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
	if err != nil || result.Skipped != len(items) || !bytes.Equal(before, after) {
		t.Fatal("same-repository exported snapshot wrote unexpectedly", result, err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("GitHub import/export opened SQLite", err)
	}
	t.Setenv("TD_SCHEDULE_FAIL_WRITE", "1")
	partialInput := `[{"issue":{"id":"td-partial","title":"Partial"}}]`
	_, err = execute(partialInput, "--source", "project-b")
	if err == nil || !strings.Contains(err.Error(), "HTTP 403 write denied") || !strings.Contains(err.Error(), "created=1") || !strings.Contains(err.Error(), "no automatic retry") {
		t.Fatal("CLI hid partial-write counters/original failure", err)
	}
	t.Setenv("TD_SCHEDULE_FAIL_WRITE", "")
	result, err = execute(partialInput, "--source", "project-b")
	if err != nil || result.Created != 0 || result.Completed != 1 {
		t.Fatal("CLI partial import did not resume allocated target", result, err)
	}
}

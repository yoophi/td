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
	"reflect"
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
	input := `[{"issue":{"id":"td-parent","title":"Parent","type":"epic","description":"Original description","acceptance":"Original acceptance","priority":"P1","points":3,"sprint":"Sprint A","minor":true,"due_date":"2026-10-20","defer_until":"2026-10-15","defer_count":2},"files":[{"id":"file-old","issue_id":"td-parent","file_path":"src/example.go","role":"implementation","linked_at":"2026-10-01T12:00:00Z"}],"logs":[{"id":"log-old","session_id":"old-worker","message":"Original progress","type":"progress","timestamp":"2026-10-01T12:00:00Z"}],"handoffs":[{"id":"handoff-old","session_id":"old-worker","done":["Original handoff"],"timestamp":"2026-10-01T12:00:00Z"}]},{"issue":{"id":"td-child","title":"Child","parent_id":"td-parent"},"dependencies":["td-parent"]}]`
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
	parentID := ""
	for _, item := range items {
		if item.Issue.Title == "Parent" {
			parentID = item.Issue.ID
			found = len(item.Logs) == 1 && len(item.Handoffs) == 1 && item.Logs[0].OriginalSessionID == "old-worker"
			if item.Issue.Description != "Original description" || item.Issue.Acceptance != "Original acceptance" || item.Issue.Type != models.TypeEpic || item.Issue.Priority != models.PriorityP1 || item.Issue.Points != 3 || item.Issue.Sprint != "Sprint A" || !item.Issue.Minor || item.Issue.DueDate == nil || *item.Issue.DueDate != "2026-10-20" || item.Issue.DeferUntil == nil || *item.Issue.DeferUntil != "2026-10-15" || item.Issue.DeferCount != 2 || len(item.Files) != 1 || item.Files[0].FilePath != "src/example.go" || item.Files[0].IssueID != item.Issue.ID {
				t.Fatal("CLI round trip lost portable fields/files", item)
			}
		}
	}
	if !found {
		t.Fatal("CLI export lost original portable history")
	}
	for _, item := range items {
		if item.Issue.Title == "Child" && (item.Issue.ParentID != parentID || len(item.Dependencies) != 1 || item.Dependencies[0].DependsOnID != parentID || item.Dependencies[0].IssueID != item.Issue.ID) {
			t.Fatal("CLI round trip lost ID mapping/relationships", item)
		}
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
			stored, err := database.GetIssue(db.NormalizeIssueID(item.Issue.ID))
			if err != nil || stored == nil || stored.Description != item.Issue.Description || stored.Acceptance != item.Issue.Acceptance || stored.Priority != item.Issue.Priority || stored.Points != item.Issue.Points || !reflect.DeepEqual(stored.DueDate, item.Issue.DueDate) || !reflect.DeepEqual(stored.DeferUntil, item.Issue.DeferUntil) {
				t.Fatal("SQLite restore lost portable fields", stored, err)
			}
			files, err := database.GetLinkedFiles(db.NormalizeIssueID(item.Issue.ID))
			if err != nil || len(files) != 1 || files[0].FilePath != "src/example.go" || files[0].IssueID != db.NormalizeIssueID(item.Issue.ID) {
				t.Fatal("SQLite restore lost mapped files", files, err)
			}
			logs, err := database.GetLogs(db.NormalizeIssueID(item.Issue.ID), 0)
			if err != nil || len(logs) != 1 || logs[0].Message != "Original progress" || !strings.HasPrefix(logs[0].SessionID, "imported-") {
				t.Fatal("SQLite restore lost content or reused active actor", logs, err)
			}
			handoff, err := database.GetLatestHandoff(db.NormalizeIssueID(item.Issue.ID))
			if err != nil || handoff == nil || len(handoff.Done) != 1 || handoff.Done[0] != "Original handoff" {
				t.Fatal("SQLite restore lost original handoff", handoff, err)
			}
		}
		if item.Issue.Title == "Child" {
			stored, err := database.GetIssue(db.NormalizeIssueID(item.Issue.ID))
			deps, depErr := database.GetDependencies(db.NormalizeIssueID(item.Issue.ID))
			if err != nil || depErr != nil || stored.ParentID != db.NormalizeIssueID(parentID) || !reflect.DeepEqual(deps, []string{db.NormalizeIssueID(parentID)}) {
				t.Fatal("SQLite restore lost mapped relationships", stored, deps, err, depErr)
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

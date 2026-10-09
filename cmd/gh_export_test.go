package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
	"github.com/spf13/cobra"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type exportFixture struct {
	rows         []ghstore.ExportedIssue
	err          error
	all, history bool
}

func (f *exportFixture) ExportIssues(_ context.Context, all, history bool) ([]ghstore.ExportedIssue, error) {
	f.all, f.history = all, history
	return f.rows, f.err
}
func exportCommand(format, path string) *cobra.Command {
	c := &cobra.Command{Use: "export"}
	c.Flags().String("format", format, "")
	c.Flags().String("output", path, "")
	c.Flags().Bool("all", false, "")
	c.Flags().Bool("render-markdown", false, "")
	return c
}
func TestGitHubExportPreservesSharedDataAndCommonFormat(t *testing.T) {
	now := time.Now().UTC()
	f := &exportFixture{rows: []ghstore.ExportedIssue{{Version: 1, Repository: "owner/repo", Body: "native body", Author: "actual-user", Record: ghstore.Record{Number: 1, URL: "https://github.com/owner/repo/issues/1", Issue: models.Issue{ID: "gh-1", Title: "한글 📝", DeletedAt: &now, DueDate: new("2026-10-10")}, Details: &ghstore.IssueDetails{Dependencies: []string{"gh-2"}, Files: []models.IssueFile{{IssueID: "gh-1", FilePath: "src/main.go"}}, Reviews: []models.IssueReview{{ID: "review-1"}}}}, Activity: []models.Activity{{ID: "ghc-10", IssueID: "gh-1", Kind: "log", Message: "progress", Author: "peer", CreatedAt: now}, {ID: "ghc-11", IssueID: "gh-1", Kind: "handoff", Done: []string{"done"}, CreatedAt: now}, {ID: "ghc-12", Kind: "comment", Native: true, Author: "native-peer"}}}}}
	c := exportCommand("json", "")
	var out bytes.Buffer
	c.SetOut(&out)
	_ = c.Flags().Set("all", "true")
	if err := runGitHubExport(c, f); err != nil {
		t.Fatal(err)
	}
	var rows []exportedItem
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if !f.all || !f.history || len(rows) != 1 || rows[0].GitHub.Author != "actual-user" || rows[0].GitHub.Activity[2].Author != "native-peer" || rows[0].GitHub.Record.Details.Reviews[0].ID != "review-1" || rows[0].Issue.DeletedAt == nil || len(rows[0].Logs) != 1 || len(rows[0].Handoffs) != 1 || len(rows[0].Dependencies) != 1 || len(rows[0].Files) != 1 {
		t.Fatalf("lost export fields: %+v", rows)
	}
	encoded, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	var again []exportedItem
	if err := json.Unmarshal(encoded, &again); err != nil || again[0].GitHub.Body != "native body" {
		t.Fatal("archive decode roundtrip lost original body", err)
	}
}
func TestGitHubExportFailureDoesNotReplaceBackupOrEmitPartialJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backup.json")
	if err := os.WriteFile(path, []byte("old backup"), 0600); err != nil {
		t.Fatal(err)
	}
	f := &exportFixture{err: errors.New("HTTP 403: API rate limit exceeded")}
	c := exportCommand("json", path)
	var out bytes.Buffer
	c.SetOut(&out)
	if err := runGitHubExport(c, f); err == nil {
		t.Fatal("failed read accepted")
	}
	content, _ := os.ReadFile(path)
	if string(content) != "old backup" || out.Len() != 0 {
		t.Fatal("partial backup exposed")
	}
	f.err = nil
	if err := runGitHubExport(c, f); err != nil {
		t.Fatal(err)
	}
	content, _ = os.ReadFile(path)
	if string(content) != "[]" {
		t.Fatalf("empty array contract: %s", content)
	}
	files, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".td-export-*"))
	if len(files) != 0 {
		t.Fatal("temporary backup leaked")
	}
}
func TestGitHubMarkdownExportDoesNotReadActivity(t *testing.T) {
	f := &exportFixture{rows: []ghstore.ExportedIssue{{Record: ghstore.Record{Issue: models.Issue{ID: "gh-1", Title: "Markdown", Description: "body"}}}}}
	c := exportCommand("md", "")
	var out bytes.Buffer
	c.SetOut(&out)
	if err := runGitHubExport(c, f); err != nil || f.history || !strings.Contains(out.String(), "## gh-1: Markdown") {
		t.Fatal(err, out.String())
	}
}

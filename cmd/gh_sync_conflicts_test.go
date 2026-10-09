package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/db"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
	"github.com/spf13/cobra"
)

func TestGitHubSyncConflictsTypedRecordingScopeAndOfflineRead(t *testing.T) {
	dir := storeTestDir(t)
	if err := config.SetStore(dir, "gh-issue", &models.GitHubStoreConfig{Repo: "owner/repo", Remote: "origin"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TD_GH_CONTEXT_ROOT", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	if err := db.LogAgentErrorWithConflict(dir, nil, "other repository", "", &db.GitHubConflictDiagnostic{Repository: "other/repo", EntityID: "gh-1"}); err != nil {
		t.Fatal(err)
	}
	// Identical human wording is insufficient evidence for classification.
	logAgentError(nil, (&ghstore.ConflictError{ID: "gh-2"}).Error())
	logAgentFailure([]string{"update", "gh-3"}, fmt.Errorf("wrapped original: %w", &ghstore.ConflictError{ID: "gh-3"}))
	logAgentFailure([]string{"update", "gh-4"}, &ghstore.ConflictError{ID: "gh-4", AfterWrite: true})
	logged, err := db.ReadAgentErrors(dir)
	if err != nil || len(logged) != 4 || logged[1].GitHubConflict != nil || logged[2].GitHubConflict == nil || logged[2].GitHubConflict.Repository != "owner/repo" {
		t.Fatal(logged, err)
	}
	execute := func(args ...string) ([]githubConflictEntry, string, error) {
		cmd := &cobra.Command{Use: syncConflictsCmd.Use, Args: syncConflictsCmd.Args, RunE: syncConflictsCmd.RunE, SilenceErrors: true, SilenceUsage: true}
		cmd.Flags().Int("limit", 20, "")
		cmd.Flags().String("since", "", "")
		cmd.Flags().Bool("json", false, "")
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetArgs(append([]string{"--json"}, args...))
		err := cmd.Execute()
		var report struct {
			Coverage  string                `json:"coverage"`
			Conflicts []githubConflictEntry `json:"conflicts"`
		}
		if err == nil {
			if err := json.Unmarshal(out.Bytes(), &report); err != nil {
				t.Fatal(out.String(), err)
			}
		}
		return report.Conflicts, report.Coverage, err
	}
	entries, coverage, err := execute("--since", "1h")
	if err != nil || coverage != "device-local-cli" || len(entries) != 2 || entries[0].EntityID != "gh-4" || !entries[0].AfterWrite || entries[1].EntityID != "gh-3" || entries[1].AfterWrite || !strings.Contains(entries[1].Error, "wrapped original") {
		t.Fatal(entries, coverage, err)
	}
	entries, _, err = execute("--limit", "1")
	if err != nil || len(entries) != 1 || entries[0].EntityID != "gh-4" {
		t.Fatal(entries, err)
	}
	for _, args := range [][]string{{"--limit", "0"}, {"--since", "bad"}, {"--since", "-1h"}} {
		if _, _, err := execute(args...); err == nil {
			t.Fatal("invalid filter accepted", args)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("conflict diagnostics opened SQLite", err)
	}
}

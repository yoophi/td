package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/ghcontext"
	"github.com/marcus/td/internal/ghstore"
)

func journalCLI(t *testing.T) (string, string, func(...string) (string, string, error)) {
	t.Helper()
	dir := githubScheduleTestDir(t)
	t.Setenv("TD_SESSION_ID", "journal-cli-fixture")
	t.Setenv("TD_GH_CONTEXT_ROOT", t.TempDir())
	t.Setenv("TD_GH_CACHE", "off")
	t.Setenv("TD_GH_SYNC_HELPER", "1")
	t.Setenv("TD_ANALYTICS", "false")
	scope, err := ghcontext.Resolve(context.Background(), dir, "owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	state, err := scope.Update(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return dir, state.Session.ID, func(args ...string) (string, string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		child := exec.CommandContext(ctx, executable, append([]string{"-test.run=^TestGitHubSyncCLIHelperProcess$", "--", "--work-dir", dir}, args...)...)
		child.Dir = dir
		var stdout, stderr bytes.Buffer
		child.Stdout, child.Stderr = &stdout, &stderr
		err := child.Run()
		return stdout.String(), stderr.String(), err
	}
}

func fixtureJournals(t *testing.T) []ghstore.ActionJournal {
	t.Helper()
	data, err := os.ReadFile(os.Getenv("TD_GH_FIXTURE_JOURNAL"))
	if err != nil {
		t.Fatal(err)
	}
	var remote struct{ Comments []struct{ Body string } }
	if err := json.Unmarshal(data, &remote); err != nil {
		t.Fatal(err)
	}
	result := []ghstore.ActionJournal{}
	for _, comment := range remote.Comments {
		parts := strings.SplitN(comment.Body, "<!-- td:action:v1\n", 2)
		if len(parts) != 2 {
			t.Fatal("missing action marker", comment.Body)
		}
		var action ghstore.ActionJournal
		if err := json.Unmarshal([]byte(strings.TrimSuffix(parts[1], "\n-->")), &action); err != nil {
			t.Fatal(err)
		}
		result = append(result, action)
	}
	return result
}

func fixtureTargetTitle(t *testing.T, id string) string {
	t.Helper()
	data, err := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
	if err != nil {
		t.Fatal(err)
	}
	var remote struct {
		Issues map[string]struct{ Title string }
	}
	if err := json.Unmarshal(data, &remote); err != nil {
		t.Fatal(err)
	}
	return remote.Issues[id].Title
}

func TestGitHubJournalCLITracksCreateUpdatesAndSequentialUndo(t *testing.T) {
	for _, enabled := range []string{"false", "true"} {
		t.Run(enabled, func(t *testing.T) {
			t.Setenv("TD_FEATURE_SYNC_CLI", enabled)
			dir, sessionID, run := journalCLI(t)
			execute := func(args ...string) string {
				t.Helper()
				out, stderr, err := run(args...)
				if err != nil || !json.Valid([]byte(out)) {
					t.Fatal(args, out, stderr, err)
				}
				return out
			}
			execute("task", "create", "Original task", "--json")
			execute("edit", "gh-6", "--title", "First title", "--json")
			execute("update", "gh-6", "--title", "Second title", "--json")
			journals := fixtureJournals(t)
			if len(journals) != 3 || journals[0].Action != "create" || journals[0].CreateInput == nil {
				t.Fatal(journals)
			}
			for _, journal := range journals {
				if journal.Phase != "applied" || journal.SessionID != sessionID || journal.IssueID != "gh-6" || journal.CarrierID != "gh-999" || journal.After == nil {
					t.Fatal(journal)
				}
			}
			if journals[1].Before.Title != "Original task" || journals[2].Before.Title != "First title" {
				t.Fatal("before snapshots lost", journals)
			}
			var rows []githubActionHistoryRow
			if err := json.Unmarshal([]byte(execute("last", "--n", "3", "--json")), &rows); err != nil || len(rows) != 3 {
				t.Fatal(rows, err)
			}
			for _, want := range []string{"First title", "Original task"} {
				if out := execute("undo", "--json"); !strings.Contains(out, `"action":"undone"`) || fixtureTargetTitle(t, "6") != want {
					t.Fatal(out, want)
				}
			}
			journals = fixtureJournals(t)
			if journals[0].Phase != "applied" || journals[1].Phase != "undone" || journals[2].Phase != "undone" {
				t.Fatal(journals)
			}
			out, stderr, err := run("undo", "--json")
			if err == nil || !strings.Contains(out+stderr, "logical delete") || fixtureTargetTitle(t, "6") != "Original task" {
				t.Fatal("creation erased", out, stderr, err)
			}
			if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
				t.Fatal("SQLite fallback")
			}
		})
	}
}

func TestGitHubJournalCLIReportsIntentTargetAndReceiptFailures(t *testing.T) {
	for _, mode := range []string{"intent-denied", "intent-lost", "target-denied", "target-lost", "receipt-denied", "receipt-lost"} {
		t.Run(mode, func(t *testing.T) {
			dir, _, run := journalCLI(t)
			if out, stderr, err := run("create", "Original task", "--json"); err != nil {
				t.Fatal(out, stderr, err)
			}
			t.Setenv("TD_GH_JOURNAL_FAIL", mode)
			if mode == "target-denied" {
				t.Setenv("TD_SCHEDULE_FAIL_WRITE", "1")
			}
			out, stderr, err := run("update", "gh-6", "--title", "Updated task", "--json")
			if err == nil || strings.Contains(out, `"action":"updated"`) {
				t.Fatal("unverified write succeeded", out, stderr, err)
			}
			wantError := map[string]string{"intent-denied": "HTTP 403 journal intent denied", "intent-lost": "connection lost after intent", "target-denied": "HTTP 403 write denied", "target-lost": "connection lost after target", "receipt-denied": "HTTP 403 journal receipt denied", "receipt-lost": "connection lost after receipt"}[mode]
			if !strings.Contains(out+stderr, wantError) {
				t.Fatal("raw error lost", out, stderr)
			}
			wantTitle := "Original task"
			if mode == "target-lost" || strings.HasPrefix(mode, "receipt-") {
				wantTitle = "Updated task"
			}
			if fixtureTargetTitle(t, "6") != wantTitle {
				t.Fatal("wrong target outcome", mode)
			}
			journals := fixtureJournals(t)
			wantCount := 2
			if mode == "intent-denied" {
				wantCount = 1
			}
			if len(journals) != wantCount {
				t.Fatal("automatic retry or missing intent", journals)
			}
			if len(journals) == 2 {
				wantPhase := "prepared"
				if mode == "receipt-lost" {
					wantPhase = "applied"
				}
				if journals[1].Phase != wantPhase {
					t.Fatal(journals[1])
				}
				if strings.HasPrefix(mode, "target-") && (!strings.Contains(out+stderr, journals[1].OperationID) || !strings.Contains(out+stderr, "inspect target and journal")) {
					t.Fatal("unverified target result lacks reconciliation context", out, stderr)
				}

			}
			if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
				t.Fatal("SQLite fallback")
			}
		})
	}
}

func TestGitHubJournalCLIBootstrapValidationAndHumanOutput(t *testing.T) {
	t.Setenv("TD_GH_FIXTURE_NO_CARRIER", "1")
	_, _, run := journalCLI(t)
	if out, stderr, err := run("show", "gh-1", "--json"); err != nil {
		t.Fatal(out, stderr, err)
	}
	if out, stderr, err := run("create", "--title", "", "--json"); err == nil {
		t.Fatal("invalid input accepted", out, stderr)
	}
	if _, err := os.Stat(os.Getenv("TD_GH_FIXTURE_JOURNAL")); !os.IsNotExist(err) {
		t.Fatal("read/invalid input bootstrapped a journal", err)
	}
	if out, stderr, err := run("create", "Original task"); err != nil || !strings.Contains(out, "CREATED gh-6: Original task") {
		t.Fatal(out, stderr, err)
	}
	if out, stderr, err := run("edit", "gh-6", "--title", "Edited task"); err != nil || !strings.Contains(out, "UPDATED gh-6: Edited task") {
		t.Fatal(out, stderr, err)
	}
	if out, stderr, err := run("undo"); err != nil || !strings.Contains(out, "UNDONE:") || fixtureTargetTitle(t, "6") != "Original task" {
		t.Fatal(out, stderr, err)
	}
	if out, stderr, err := run("list", "--all", "--json"); err != nil || strings.Contains(out, `"id":"gh-999"`) {
		t.Fatal("journal carrier exposed as a task", out, stderr, err)
	}
	journals := fixtureJournals(t)
	if len(journals) != 2 || journals[0].Phase != "applied" || journals[1].Phase != "undone" {
		t.Fatal(journals)
	}
}

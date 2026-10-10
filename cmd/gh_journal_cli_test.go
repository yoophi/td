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

func fixtureAllJournals(t *testing.T) []ghstore.ActionJournal {
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
		parts := strings.SplitN(comment.Body, "<!-- td:action:", 2)
		if len(parts) == 2 {
			parts[1] = strings.SplitN(parts[1], "\n", 2)[1]
		}
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

// Existing assertions count issue effects independently of activity effects.
func fixtureJournals(t *testing.T) []ghstore.ActionJournal {
	t.Helper()
	result := []ghstore.ActionJournal{}
	for _, action := range fixtureAllJournals(t) {
		if action.Action != "comment_create" {
			result = append(result, action)
		}
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

func TestGitHubJournalCLITracksWorkflowDeletionRelationsScheduleFilesAndWorkSessions(t *testing.T) {
	dir, sessionID, run := journalCLI(t)
	if err := os.WriteFile(filepath.Join(dir, "linked.go"), []byte("package fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	execute := func(args ...string) {
		t.Helper()
		out, stderr, err := run(append(args, "--json")...)
		if err != nil || !json.Valid([]byte(out)) {
			t.Fatal(args, out, stderr, err)
		}
	}
	commands := [][]string{
		{"create", "Journal workflow fixture"},
		{"start", "gh-6"}, {"unstart", "gh-6"}, {"start", "gh-6"},
		{"block", "gh-6", "--reason", "fixture blocker"}, {"unblock", "gh-6"},
		{"due", "gh-6", "2026-12-01"}, {"defer", "gh-6", "2026-12-02"},
		{"dep", "add", "gh-6", "gh-1"}, {"dep", "rm", "gh-6", "gh-1"},
		{"link", "gh-6", "linked.go"}, {"unlink", "gh-6", "linked.go"},
		{"delete", "gh-6", "--yes"}, {"restore", "gh-6"},
	}
	for i, args := range commands {
		execute(args...)
		journals := fixtureJournals(t)
		if len(journals) != i+1 {
			t.Fatal("missing or duplicate issue write journal", args, journals)
		}
		last := journals[len(journals)-1]
		if last.SessionID != sessionID || last.Phase != "applied" || last.IssueID != "gh-6" || last.After == nil || (i > 0 && last.Before == nil) {
			t.Fatal(args, last)
		}
	}
	// No-op transitions/deletion must not generate fictitious remote actions.
	execute("start", "gh-6")
	count := len(fixtureJournals(t))
	execute("start", "gh-6")
	if len(fixtureJournals(t)) != count {
		t.Fatal("no-op generated an action")
	}
	execute("unstart", "gh-6")
	execute("ws", "start", "Journal bundle")
	execute("ws", "tag", "gh-6")
	execute("ws", "handoff", "--review", "--done", "fixture handoff")
	if len(fixtureJournals(t)) != count+3 {
		t.Fatal("work session transitions were not journaled", fixtureJournals(t))
	}
	last := fixtureJournals(t)
	originalBody := last[len(last)-1].Before.Body
	out, stderr, err := run("undo", "--json")
	if err != nil || !json.Valid([]byte(out)) {
		t.Fatal("work-session review inverse failed", out, stderr, err)
	}
	after, err := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
	if err != nil {
		t.Fatal(err)
	}
	var restored struct {
		Issues map[string]struct{ Body string }
	}
	if err := json.Unmarshal(after, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Issues["6"].Body != originalBody {
		t.Fatal("review inverse lost original state")
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("SQLite fallback")
	}
}

func TestGitHubJournalCLIUndoesFileLinkAndUnlinkAfterOwnAuditLog(t *testing.T) {
	for _, enabled := range []string{"false", "true"} {
		t.Run(enabled, func(t *testing.T) {
			t.Setenv("TD_FEATURE_SYNC_CLI", enabled)
			dir, _, run := journalCLI(t)
			if err := os.WriteFile(filepath.Join(dir, "linked.go"), []byte("package fixture\n"), 0600); err != nil {
				t.Fatal(err)
			}
			execute := func(args ...string) {
				t.Helper()
				out, stderr, err := run(append(args, "--json")...)
				if err != nil || !json.Valid([]byte(out)) {
					t.Fatal(args, out, stderr, err)
				}
			}
			execute("create", "File undo CLI fixture")
			for _, unlink := range []bool{false, true} {
				if unlink {
					execute("link", "gh-6", "linked.go", "--role", "test")
				}
				var before map[string]any
				data, err := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(data, &before); err != nil {
					t.Fatal(err)
				}
				body := before["issues"].(map[string]any)["6"].(map[string]any)["body"]
				if unlink {
					execute("unlink", "gh-6", "linked.go")
				} else {
					execute("link", "gh-6", "linked.go", "--role", "test")
				}
				// File commands have two distinct tracked effects: issue metadata,
				// then their own audit comment. Undo them in reverse order.
				execute("undo")
				execute("undo")
				data, err = os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
				if err != nil {
					t.Fatal(err)
				}
				var after map[string]any
				if err := json.Unmarshal(data, &after); err != nil {
					t.Fatal(err)
				}
				if after["issues"].(map[string]any)["6"].(map[string]any)["body"] != body {
					t.Fatal("file metadata was not restored after sequential audit-comment/file undo")
				}
				journals := fixtureAllJournals(t)
				last := journals[len(journals)-1]
				if last.Phase != "undone" || last.Action != "comment_create" {
					t.Fatal("audit comment inverse not recorded", last)
				}
				last = journals[len(journals)-2]
				if last.Phase != "undone" || last.Action != "update" || last.Restored == nil {
					t.Fatal("file inverse not recorded", last)
				}
			}
			if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
				t.Fatal("unexpected SQLite fallback", err)
			}
		})
	}
}

func TestGitHubJournalCLIUndoesDependenciesAndParents(t *testing.T) {
	for _, enabled := range []string{"false", "true"} {
		t.Run(enabled, func(t *testing.T) {
			t.Setenv("TD_FEATURE_SYNC_CLI", enabled)
			dir, _, run := journalCLI(t)
			execute := func(args ...string) {
				t.Helper()
				out, stderr, err := run(append(args, "--json")...)
				if err != nil || !json.Valid([]byte(out)) {
					t.Fatal(args, out, stderr, err)
				}
			}
			body := func() string {
				t.Helper()
				data, err := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
				if err != nil {
					t.Fatal(err)
				}
				var state struct {
					Issues map[string]struct{ Body string }
				}
				if err := json.Unmarshal(data, &state); err != nil {
					t.Fatal(err)
				}
				return state.Issues["6"].Body
			}
			execute("create", "Relation undo CLI fixture")
			original := body()
			execute("dep", "add", "gh-6", "gh-1")
			execute("undo") // one source graph write; no separate log comment
			if body() != original {
				t.Fatal("dependency add inverse lost original body")
			}
			execute("dep", "add", "gh-6", "gh-1")
			original = body()
			execute("dep", "rm", "gh-6", "gh-1")
			execute("undo")
			if body() != original {
				t.Fatal("dependency remove inverse lost original body/history")
			}
			original = body()
			execute("update", "gh-6", "--parent", "gh-1")
			execute("undo")
			if body() != original {
				t.Fatal("parent inverse lost original body")
			}
			execute("update", "gh-6", "--parent", "gh-1")
			original = body()
			execute("update", "gh-6", "--parent", "")
			execute("undo")
			if body() != original {
				t.Fatal("parent clear inverse lost original body")
			}
			if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
				t.Fatal("unexpected SQLite fallback", err)
			}
		})
	}
}

func TestGitHubJournalCLITracksLabelRepairWritesButNotPreview(t *testing.T) {
	_, sessionID, run := journalCLI(t)
	execute := func(args ...string) {
		t.Helper()
		out, stderr, err := run(append(args, "--json")...)
		if err != nil || !json.Valid([]byte(out)) {
			t.Fatal(args, out, stderr, err)
		}
	}
	execute("create", "Label journal fixture")
	remove := func(label string) {
		t.Helper()
		file := os.Getenv("TD_SCHEDULE_STATE")
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var remote map[string]any
		if err := json.Unmarshal(data, &remote); err != nil {
			t.Fatal(err)
		}
		target := remote["issues"].(map[string]any)["6"].(map[string]any)
		labels := []any{}
		for _, raw := range target["labels"].([]any) {
			if raw.(map[string]any)["name"] != label {
				labels = append(labels, raw)
			}
		}
		target["labels"] = labels
		clock, err := time.Parse(time.RFC3339, target["updated_at"].(string))
		if err != nil {
			t.Fatal(err)
		}
		target["updated_at"] = clock.Add(time.Second).Format(time.RFC3339)
		data, err = json.Marshal(remote)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	remove("td:priority:P2")
	out, stderr, err := run("config", "sync-priority-labels", "--id", "gh-6")
	if err != nil || !strings.Contains(out, "Preview: 1 repairs") || stderr != "" {
		t.Fatal("human preview must be on stdout", out, stderr, err)
	}
	execute("config", "sync-priority-labels", "--id", "gh-6")
	if len(fixtureJournals(t)) != 1 {
		t.Fatal("preview wrote journal")
	}
	execute("config", "sync-priority-labels", "--id", "gh-6", "--apply")
	if len(fixtureJournals(t)) != 2 {
		t.Fatal("priority repair missing journal")
	}
	remove("td:open")
	execute("config", "sync-state-labels")
	journals := fixtureJournals(t)
	if len(journals) != 3 || journals[2].SessionID != sessionID || journals[2].Phase != "applied" {
		t.Fatal(journals)
	}
}

func TestGitHubJournalCLIWriterReceiptFailuresRetainUnverifiedHistory(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"transition", []string{"start", "gh-6"}},
		{"delete", []string{"delete", "gh-6", "--yes"}},
		{"dependency", []string{"dep", "add", "gh-6", "gh-1"}},
		{"schedule", []string{"due", "gh-6", "2026-12-01"}},
		{"files", []string{"link", "gh-6", "linked.go"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, sessionID, run := journalCLI(t)
			if err := os.WriteFile(filepath.Join(dir, "linked.go"), []byte("package fixture\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if out, stderr, err := run("create", "Writer failure fixture", "--json"); err != nil {
				t.Fatal(out, stderr, err)
			}
			t.Setenv("TD_GH_JOURNAL_FAIL", "receipt-denied")
			out, stderr, err := run(append(tc.args, "--json")...)
			if err == nil || !strings.Contains(out+stderr, "HTTP 403 journal receipt denied") || !strings.Contains(out+stderr, "unverified") {
				t.Fatal("unverified receipt reported success", tc.name, out, stderr, err)
			}
			journals := fixtureJournals(t)
			if len(journals) != 2 || journals[1].Phase != "prepared" || journals[1].SessionID != sessionID || journals[1].After != nil {
				t.Fatal("failed receipt falsely completed or retried", journals)
			}
			before, err := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
			if err != nil {
				t.Fatal(err)
			}
			out, stderr, err = run("undo", "--json")
			if err == nil || !strings.Contains(out+stderr, "unverified outcome") {
				t.Fatal("pending action skipped", out, stderr, err)
			}
			after, err := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("pending action undo wrote target")
			}
		})
	}
}

// A comment may advance the native issue clock without changing title/body.
// It must not become an implicit exemption from revision conflict detection.
func TestGitHubJournalCLIUndoRejectsUnjournaledCommentClock(t *testing.T) {
	_, _, run := journalCLI(t)
	for _, args := range [][]string{{"create", "Comment clock fixture"}, {"due", "gh-6", "2026-12-01"}} {
		out, stderr, err := run(append(args, "--json")...)
		if err != nil {
			t.Fatal(args, out, stderr, err)
		}
	}
	// Simulate a native/pre-journal comment: remove its receipt, leaving the
	// actual comment and advanced issue clock intact.
	journalFile := os.Getenv("TD_GH_FIXTURE_JOURNAL")
	encoded, err := os.ReadFile(journalFile)
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Comments []map[string]any `json:"comments"`
	}
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatal(err)
	}
	kept := []map[string]any{}
	for _, comment := range raw.Comments {
		if !strings.Contains(comment["body"].(string), "<!-- td:action:v2") {
			kept = append(kept, comment)
		}
	}
	raw.Comments = kept
	encoded, err = json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(journalFile, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
	if err != nil {
		t.Fatal(err)
	}
	journals := fixtureJournals(t)
	var state struct {
		Issues map[string]struct {
			UpdatedAt time.Time `json:"updated_at"`
		} `json:"issues"`
	}
	if err := json.Unmarshal(before, &state); err != nil {
		t.Fatal(err)
	}
	if !state.Issues["6"].UpdatedAt.After(journals[1].After.UpdatedAt) {
		t.Fatal("fixture comment did not advance target revision")
	}
	out, stderr, err := run("undo", "--json")
	if err == nil || !strings.Contains(out+stderr, "conflict") {
		t.Fatal("unjournaled clock bypass", out, stderr, err)
	}
	after, err := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || fixtureJournals(t)[1].Phase != "applied" {
		t.Fatal("conflict mutated target or receipt")
	}
}

func TestGitHubJournalCLITracksDirectAutomaticAndFanoutActivities(t *testing.T) {
	_, sessionID, run := journalCLI(t)
	for _, args := range [][]string{
		{"create", "First activity target"}, {"create", "Second activity target"},
		{"comment", "gh-6", "Direct comment"}, {"comments", "add", "gh-6", "Nested comment"},
		{"log", "--issue", "gh-6", "Saved log"}, {"handoff", "gh-6", "--done", "Saved handoff"},
		{"due", "gh-6", "2026-12-01"}, {"update", "gh-6", "--title", "Updated target", "--comment", "Update explanation"},
		{"ws", "start", "Fanout activity"}, {"ws", "tag", "gh-6", "gh-7", "--no-start"},
		{"ws", "log", "Shared work-session log"},
	} {
		out, stderr, err := run(append(args, "--json")...)
		if err != nil || !json.Valid([]byte(out)) {
			t.Fatal(args, out, stderr, err)
		}
	}
	comments := []ghstore.ActionJournal{}
	for _, action := range fixtureAllJournals(t) {
		if action.Action == "comment_create" {
			if action.Phase != "applied" || action.SessionID != sessionID || action.CommentAfter == nil || action.CommentAfter.Body != action.CommentInput.Body || action.CommentAfter.Author != "fixture-author" {
				t.Fatal("missing verified native comment", action)
			}
			comments = append(comments, action)
		}
	}
	if len(comments) != 8 {
		t.Fatal("direct/automatic/fanout activity not journaled", comments)
	}
	first, second := comments[6], comments[7]
	if first.CommentInput.OperationID != second.CommentInput.OperationID || first.OperationID == second.OperationID || first.IssueID == second.IssueID {
		t.Fatal("work-session fanout identity corrupted", first, second)
	}
	out, stderr, err := run("undo", "--json")
	if err != nil || !json.Valid([]byte(out)) {
		t.Fatal("guarded comment undo failed", out, stderr, err)
	}
	actions := fixtureAllJournals(t)
	last := actions[len(actions)-1]
	if last.Action != "comment_create" || last.Phase != "undone" || last.CommentAfter.Body != second.CommentAfter.Body || last.Restored == nil {
		t.Fatal("comment undo did not preserve its original audit state", last)
	}
}

func TestGitHubJournalCLICommentReceiptFailurePreservesNativeComment(t *testing.T) {
	_, _, run := journalCLI(t)
	if out, stderr, err := run("create", "Comment receipt target", "--json"); err != nil {
		t.Fatal(out, stderr, err)
	}
	t.Setenv("TD_GH_JOURNAL_FAIL", "receipt-denied")
	out, stderr, err := run("comment", "gh-6", "Saved before receipt failure", "--json")
	if err == nil || !strings.Contains(out+stderr, "HTTP 403 journal receipt denied") || !strings.Contains(out+stderr, "unverified") {
		t.Fatal("comment partial result hidden", out, stderr, err)
	}
	actions := fixtureAllJournals(t)
	if len(actions) != 2 || actions[1].Action != "comment_create" || actions[1].Phase != "prepared" || actions[1].CommentAfter != nil {
		t.Fatal("false applied receipt", actions)
	}
	data, err := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
	if err != nil {
		t.Fatal(err)
	}
	var remote struct {
		Comments []struct{ Body string }
	}
	if err := json.Unmarshal(data, &remote); err != nil || len(remote.Comments) != 1 || !strings.Contains(remote.Comments[0].Body, "Saved before receipt failure") {
		t.Fatal("comment missing or automatically retried", remote, err)
	}
	out, stderr, err = run("undo", "--json")
	if err == nil || !strings.Contains(out+stderr, "unverified outcome") {
		t.Fatal("partial activity skipped", out, stderr, err)
	}
}

func TestGitHubJournalCLIUndoThroughCommentReceipts(t *testing.T) {
	_, _, run := journalCLI(t)
	for _, args := range [][]string{
		{"create", "Sequential comment target"},
		{"update", "gh-6", "--title", "Scalar change"},
		{"comment", "gh-6", "First comment"},
		{"comment", "gh-6", "Second comment"},
		{"undo"}, {"undo"}, {"undo"},
	} {
		out, stderr, err := run(append(args, "--json")...)
		if err != nil || !json.Valid([]byte(out)) {
			t.Fatal(args, out, stderr, err)
		}
	}
	actions := fixtureAllJournals(t)
	if len(actions) != 4 || actions[1].Phase != "undone" || actions[2].Phase != "undone" || actions[3].Phase != "undone" {
		t.Fatal(actions)
	}
	out, stderr, err := run("show", "gh-6", "--json")
	if err != nil || !strings.Contains(out, "Sequential comment target") || strings.Contains(out, "Scalar change") {
		t.Fatal(out, stderr, err)
	}
}

func TestGitHubJournalCLITracksAuxiliaryEntityChangesAndHistoryIdentity(t *testing.T) {
	_, actor, run := journalCLI(t)
	for _, args := range [][]string{
		{"board", "create", "Journal board", "--query", "status = open"},
		{"board", "edit", "bd-gh-6", "--name", "Renamed journal board"},
		{"board", "edit", "bd-gh-6", "--view-mode", "backlog"},
		{"board", "move", "bd-gh-6", "gh-1", "1"},
		{"board", "unposition", "bd-gh-6", "gh-1"},
		{"board", "delete", "bd-gh-6"},
		{"note", "add", "Journal note", "--content", "Original content"},
		{"note", "edit", "nt-gh-7", "--title", "Renamed journal note", "--content", "Changed content"},
		{"note", "pin", "nt-gh-7"}, {"note", "unpin", "nt-gh-7"},
		{"note", "archive", "nt-gh-7"},
		{"note", "unarchive", "nt-gh-7"},
		{"note", "delete", "nt-gh-7"}, {"note", "restore", "nt-gh-7"},
	} {
		out, stderr, err := run(append(args, "--json")...)
		if err != nil || !json.Valid([]byte(out)) {
			t.Fatal(args, out, stderr, err)
		}
	}
	actions := fixtureAllJournals(t)
	if len(actions) != 14 {
		t.Fatal(actions)
	}
	for _, a := range actions {
		if a.Phase != "applied" || a.SessionID != actor {
			t.Fatal(a)
		}
	}
	out, stderr, err := run("last", "--n", "20", "--json")
	var rows []githubActionHistoryRow
	if err != nil || json.Unmarshal([]byte(out), &rows) != nil || len(rows) != 14 {
		t.Fatal(out, stderr, err)
	}
	for _, row := range rows {
		switch row.EntityType {
		case "note":
			if row.EntityID != "nt-gh-7" || row.TargetIssueID != "gh-7" {
				t.Fatal(row)
			}
		case "board":
			if row.EntityID != "bd-gh-6" || row.TargetIssueID != "gh-6" {
				t.Fatal(row)
			}
		default:
			t.Fatal("auxiliary history leaked as task", row)
		}
	}
	out, stderr, err = run("undo", "--json")
	if err != nil {
		t.Fatal(out, stderr, err)
	}
	actions = fixtureAllJournals(t)
	if actions[13].Phase != "undone" || actions[13].Restored == nil {
		t.Fatal(actions[13])
	}

}

func TestGitHubJournalCLIAuxiliarySequentialUndo(t *testing.T) {
	for _, kind := range []string{"note", "board"} {
		t.Run(kind, func(t *testing.T) {
			_, _, run := journalCLI(t)
			commands := [][]string{{"note", "add", "Original note", "--content", "Original content"}, {"note", "edit", "nt-gh-6", "--title", "Changed note", "--content", "Changed content"}, {"note", "pin", "nt-gh-6"}, {"note", "archive", "nt-gh-6"}, {"note", "delete", "nt-gh-6"}}
			if kind == "board" {
				commands = [][]string{{"board", "create", "Original board", "--query", "status = open"}, {"board", "edit", "bd-gh-6", "--name", "Changed board"}, {"board", "edit", "bd-gh-6", "--view-mode", "backlog"}, {"board", "move", "bd-gh-6", "gh-1", "1"}, {"board", "delete", "bd-gh-6"}}
			}
			for _, args := range commands {
				out, stderr, err := run(append(args, "--json")...)
				if err != nil {
					t.Fatal(args, out, stderr, err)
				}
			}
			for range len(commands) - 1 {
				out, stderr, err := run("undo", "--json")
				if err != nil {
					t.Fatal(out, stderr, err)
				}
			}
			actions := fixtureAllJournals(t)
			for _, a := range actions[1:] {
				if a.Phase != "undone" {
					t.Fatal(a)
				}
			}
			out, stderr, err := run("undo", "--json")
			if err == nil || !strings.Contains(out+stderr, "unsupported_operation") || !strings.Contains(out+stderr, "creation") {
				t.Fatal("creation was erased", out, stderr, err)
			}
			if kind == "note" {
				out, stderr, err = run("note", "show", "nt-gh-6", "--json")
				if err != nil || !strings.Contains(out, "Original content") || !strings.Contains(out, "Original note") || strings.Contains(out, "Changed content") {
					t.Fatal(out, stderr, err)
				}
			} else {
				out, stderr, err = run("board", "list", "--json")
				if err != nil || !strings.Contains(out, "Original board") || strings.Contains(out, "Changed board") || strings.Contains(out, "backlog") {
					t.Fatal(out, stderr, err)
				}
			}
		})
	}
}

func TestGitHubJournalCLIUndoesBasicWorkflow(t *testing.T) {
	for _, enabled := range []string{"false", "true"} {
		t.Run(enabled, func(t *testing.T) {
			t.Setenv("TD_FEATURE_SYNC_CLI", enabled)
			dir, _, run := journalCLI(t)
			execute := func(args ...string) {
				t.Helper()
				out, stderr, err := run(append(args, "--json")...)
				if err != nil || !json.Valid([]byte(out)) {
					t.Fatal(args, out, stderr, err)
				}
			}
			body := func() string {
				t.Helper()
				data, err := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
				if err != nil {
					t.Fatal(err)
				}
				var state struct {
					Issues map[string]struct{ Body string }
				}
				if err := json.Unmarshal(data, &state); err != nil {
					t.Fatal(err)
				}
				return state.Issues["6"].Body
			}
			execute("create", "Workflow undo CLI fixture")
			for _, action := range []string{"start", "unstart", "block", "unblock"} {
				if action == "unstart" {
					execute("start", "gh-6")
				}
				if action == "unblock" {
					execute("block", "gh-6", "--reason", "Setup")
				}
				original := body()
				args := []string{action, "gh-6", "--reason", "Undo fixture"}
				execute(args...)
				execute("undo")
				if body() != original {
					t.Fatal(action, "inverse lost original workflow body")
				}
			}
			if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
				t.Fatal("SQLite fallback", err)
			}
		})
	}
}

func TestGitHubJournalCLIUndoesReviewTransitions(t *testing.T) {
	for _, enabled := range []string{"false", "true"} {
		t.Run(enabled, func(t *testing.T) {
			t.Setenv("TD_FEATURE_SYNC_CLI", enabled)
			dir, _, run := journalCLI(t)
			execute := func(args ...string) {
				t.Helper()
				out, stderr, err := run(append(args, "--json")...)
				if err != nil || !json.Valid([]byte(out)) {
					t.Fatal(args, out, stderr, err)
				}
			}
			body := func() string {
				t.Helper()
				data, err := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
				if err != nil {
					t.Fatal(err)
				}
				var state struct {
					Issues map[string]struct{ Body string }
				}
				if err := json.Unmarshal(data, &state); err != nil {
					t.Fatal(err)
				}
				return state.Issues["6"].Body
			}
			execute("create", "Review inverse CLI fixture")
			execute("start", "gh-6")
			execute("handoff", "gh-6", "--done", "Synthetic fixture reviewed")
			original := body()
			execute("review", "gh-6")
			execute("undo")
			if body() != original {
				t.Fatal("review inverse lost original body")
			}
			execute("review", "gh-6")
			original = body()
			execute("approve", "gh-6", "--record-only", "--self-review", "--reason", "Synthetic fixture self-review")
			execute("undo")
			if body() != original {
				t.Fatal("record-only approval inverse lost original body")
			}
			execute("reject", "gh-6", "--reason", "Synthetic fixture correction")
			execute("undo")
			if body() != original {
				t.Fatal("rejection inverse lost original checkpoint")
			}
			execute("approve", "gh-6", "--record-only", "--self-review", "--reason", "Synthetic fixture self-review")
			original = body()
			execute("approve", "gh-6", "--record-only", "--decision", "changes_requested", "--self-review", "--reason", "Synthetic fixture changes requested")
			execute("undo")
			if body() != original {
				t.Fatal("changes_requested inverse lost original approval")
			}
			if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
				t.Fatal("SQLite fallback", err)
			}
		})
	}
}

func TestGitHubJournalCLIReconcilesUncertainUndoWithoutReplay(t *testing.T) {
	for _, enabled := range []string{"false", "true"} {
		t.Run(enabled, func(t *testing.T) {
			t.Setenv("TD_FEATURE_SYNC_CLI", enabled)
			dir, _, run := journalCLI(t)
			execute := func(args ...string) string {
				t.Helper()
				out, stderr, err := run(append(args, "--json")...)
				if err != nil || !json.Valid([]byte(out)) {
					t.Fatal(args, out, stderr, err)
				}
				return out
			}
			execute("create", "Undo observation CLI fixture")
			execute("update", "gh-6", "--description", "Updated fixture")
			t.Setenv("TD_GH_JOURNAL_FAIL", "undo-completion-denied")
			out, stderr, err := run("undo", "--json")
			if err == nil || !strings.Contains(out+stderr, "unverified") {
				t.Fatal(out, stderr, err)
			}
			t.Setenv("TD_GH_JOURNAL_FAIL", "")

			latest := execute("last", "--n", "1")
			var rows []struct {
				ID    string `json:"id"`
				Phase string `json:"phase"`
			}
			if err := json.Unmarshal([]byte(latest), &rows); err != nil || len(rows) != 1 || rows[0].Phase != "undo_prepared" {
				t.Fatal("not the uncertain inverse", latest, err)
			}
			beforeRecovery, err := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
			if err != nil {
				t.Fatal(err)
			}
			preview := execute("undo", "--reconcile", rows[0].ID)
			if !strings.Contains(preview, `"applied":false`) {
				t.Fatal(preview)
			}
			observedInverse := execute("undo", "--reconcile", rows[0].ID, "--apply")
			if !strings.Contains(observedInverse, `"applied":true`) || !strings.Contains(observedInverse, `"target_write_verified":false`) || !strings.Contains(observedInverse, `"causal_undo_chain_eligible":false`) {
				t.Fatal(observedInverse)
			}
			execute("undo", "--reconcile", rows[0].ID, "--apply")
			afterRecovery, err := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(beforeRecovery, afterRecovery) {
				t.Fatal("recovery replayed target")
			}
			// A new reversible change verifies the read-only completed-receipt path.
			execute("update", "gh-6", "--title", "Completed inverse fixture")
			undone := execute("undo")
			var result struct {
				ReceiptID string `json:"receipt_id"`
			}
			if err := json.Unmarshal([]byte(undone), &result); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
			if err != nil {
				t.Fatal(err)
			}
			observed := execute("undo", "--reconcile", result.ReceiptID, "--apply")
			if !strings.Contains(observed, `"already_reconciled":true`) || !strings.Contains(observed, `"target_write_replayed":false`) {
				t.Fatal(observed)
			}
			after, err := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("completion observation replayed target")
			}
			if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
				t.Fatal("SQLite fallback", err)
			}
		})
	}
}

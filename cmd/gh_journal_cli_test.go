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
	expectedJournals := 0
	for i, args := range commands {
		execute(args...)
		expectedJournals++
		switch args[0] {
		case "due", "defer", "link", "unlink":
			expectedJournals++ // command aggregate in addition to its field effect
		}
		journals := fixtureJournals(t)
		if len(journals) != expectedJournals {
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
				// One command restores both its file metadata and companion activity.
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
					t.Fatal("file metadata was not restored by whole-command undo")
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
			effectIndex := 1
			if tc.name == "schedule" || tc.name == "files" {
				effectIndex = 2
				if len(journals) != 3 || journals[1].Action != "group" || journals[1].Phase != "prepared" || journals[1].SessionID != sessionID || journals[1].Group == nil || journals[1].Group.Command != tc.args[0] || journals[1].Group.UpdateActivity == nil || journals[1].Group.Forward != nil {
					t.Fatal("failed command lost its original unverified group", journals)
				}
				if !journals[2].GroupMixed || journals[2].GroupSequence != 1 || journals[2].GroupOperationID != journals[1].OperationID {
					t.Fatal("failed first effect escaped its original command", journals)
				}
			}
			if len(journals) != effectIndex+1 || journals[effectIndex].Phase != "prepared" || journals[effectIndex].SessionID != sessionID || journals[effectIndex].After != nil {
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
	for _, args := range [][]string{{"create", "Comment clock fixture"}, {"update", "gh-6", "--title", "Before external comment"}, {"log", "gh-6", "Unjournaled activity"}} {
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
		if !strings.Contains(comment["body"].(string), `"action":"comment_create"`) {
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
	useNativeCommentOrderFixture(t)
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

func TestGitHubJournalCLIUndoesDeleteAndRestore(t *testing.T) {
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
			execute("create", "Visibility undo CLI fixture")
			original := body()
			execute("delete", "gh-6", "--reason", "Fixture deletion")
			if out, stderr, err := run("show", "gh-6", "--json"); err == nil || !strings.Contains(out+stderr, "deleted") {
				t.Fatal(out, stderr, err)
			}
			execute("undo")
			if body() != original {
				t.Fatal("delete inverse changed original body")
			}
			execute("show", "gh-6")
			execute("delete", "gh-6", "--reason", "Restore setup")
			deleted := body()
			execute("restore", "gh-6", "--reason", "Fixture restoration")
			execute("undo")
			if body() != deleted {
				t.Fatal("restore inverse changed original deletion checkpoint")
			}
			if out, stderr, err := run("show", "gh-6", "--json"); err == nil || !strings.Contains(out+stderr, "deleted") {
				t.Fatal(out, stderr, err)
			}
			if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
				t.Fatal("SQLite fallback", err)
			}
		})
	}
}

func TestGitHubJournalCLINativeCompensationAndRecovery(t *testing.T) {
	for _, enabled := range []string{"false", "true"} {
		t.Run(enabled, func(t *testing.T) {
			t.Setenv("TD_FEATURE_SYNC_CLI", enabled)
			t.Setenv("TD_GH_FIXTURE_NATIVE_EVENTS", "1")
			dir, _, run := journalCLI(t)
			execute := func(args ...string) string {
				t.Helper()
				out, stderr, err := run(append(args, "--json")...)
				if err != nil || !json.Valid([]byte(out)) {
					t.Fatal(args, out, stderr, err)
				}
				return out
			}
			execute("create", "Native compensation CLI fixture")
			execute("start", "gh-6")
			execute("handoff", "gh-6", "--done", "Synthetic fixture work")
			execute("review", "gh-6")
			execute("approve", "gh-6", "--record-only", "--self-review", "--reason", "Synthetic fixture review")
			execute("close", "gh-6", "--reason", "Close existing fixture approval")
			out := execute("undo")
			if !strings.Contains(out, `"compensation":`) || !strings.Contains(out, `"requires_new_review":true`) || !strings.Contains(out, `"causal_undo_chain_eligible":false`) {
				t.Fatal(out)
			}
			before, err := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
			if err != nil {
				t.Fatal(err)
			}
			out, stderr, err := run("approve", "gh-6", "--self-review", "--reason", "Must not reuse invalidated fixture review", "--json")
			if err == nil || !strings.Contains(out+stderr, "stale") {
				t.Fatal(out, stderr, err)
			}
			after, err := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("old review closed compensated issue", err)
			}
			// A genuinely new review cycle is allowed; the old one is not revived.
			execute("start", "gh-6")
			execute("handoff", "gh-6", "--done", "Synthetic fixture rereview work")
			execute("review", "gh-6")
			execute("approve", "gh-6", "--self-review", "--reason", "Synthetic new fixture approval")
			t.Setenv("TD_GH_JOURNAL_FAIL", "undo-completion-denied")
			out, stderr, err = run("undo", "--json")
			if err == nil || !strings.Contains(out+stderr, "unverified") {
				t.Fatal(out, stderr, err)
			}
			t.Setenv("TD_GH_JOURNAL_FAIL", "")
			var rows []struct{ ID, Phase string }
			latest := execute("last", "--n", "1")
			if err := json.Unmarshal([]byte(latest), &rows); err != nil || len(rows) != 1 || rows[0].Phase != "undo_prepared" {
				t.Fatal(latest, err)
			}
			before, err = os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
			if err != nil {
				t.Fatal(err)
			}
			execute("undo", "--reconcile", rows[0].ID)
			out = execute("undo", "--reconcile", rows[0].ID, "--apply")
			if !strings.Contains(out, `"applied":true`) || !strings.Contains(out, `"target_write_verified":false`) || !strings.Contains(out, `"causal_undo_chain_eligible":false`) {
				t.Fatal(out)
			}
			execute("undo", "--reconcile", rows[0].ID, "--apply")
			after, err = os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("native recovery replayed target", err)
			}
			execute("create", "Unreviewed native reopen fixture")
			execute("close", "gh-7", "--admin", "Synthetic explicit fixture closure")
			execute("reopen", "gh-7", "--reason", "Synthetic fixture reopening")
			out = execute("undo")
			if !strings.Contains(out, `"requires_new_review":false`) || !strings.Contains(out, `"status":"closed"`) {
				t.Fatal(out)
			}
			if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
				t.Fatal("SQLite fallback", err)
			}
		})
	}
}

func TestGitHubJournalCLIForwardUpdateRecovery(t *testing.T) {
	for _, enabled := range []string{"false", "true"} {
		t.Run(enabled, func(t *testing.T) {
			t.Setenv("TD_FEATURE_SYNC_CLI", enabled)
			t.Setenv("TD_GH_FIXTURE_NATIVE_EVENTS", "1")
			dir, _, run := journalCLI(t)
			execute := func(args ...string) string {
				t.Helper()
				out, stderr, err := run(append(args, "--json")...)
				if err != nil || !json.Valid([]byte(out)) {
					t.Fatal(args, out, stderr, err)
				}
				return out
			}
			execute("create", "Forward recovery CLI fixture")
			recoverWrite := func(args ...string) {
				t.Helper()
				t.Setenv("TD_GH_JOURNAL_FAIL", "receipt-denied")
				out, stderr, err := run(append(args, "--json")...)
				if err == nil || !strings.Contains(out+stderr, "unverified") {
					t.Fatal(args, out, stderr, err)
				}
				t.Setenv("TD_GH_JOURNAL_FAIL", "")
				var rows []struct{ ID, Phase string }
				last := execute("last", "--n", "1")
				if json.Unmarshal([]byte(last), &rows) != nil || len(rows) != 1 || rows[0].Phase != "prepared" || !strings.Contains(last, `"update_intent":`) {
					t.Fatal(last)
				}
				before, err := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
				if err != nil {
					t.Fatal(err)
				}
				execute("undo", "--reconcile", rows[0].ID)
				out = execute("undo", "--reconcile", rows[0].ID, "--apply")
				if !strings.Contains(out, `"forward_outcome":`) || !strings.Contains(out, `"target_write_verified":false`) || !strings.Contains(out, `"causal_undo_chain_eligible":false`) || !strings.Contains(out, `"update_observation":`) {
					t.Fatal(out)
				}
				execute("undo", "--reconcile", rows[0].ID, "--apply")
				after, err := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
				if err != nil || !bytes.Equal(before, after) {
					t.Fatal("forward recovery replayed target", err)
				}
			}
			recoverWrite("update", "gh-6", "--title", "Recovered scalar request")
			out := execute("undo")
			if !strings.Contains(out, `"target_write_verified":true`) || !strings.Contains(out, `"causal_undo_chain_eligible":false`) {
				t.Fatal("observed source inverse promoted to causal proof", out)
			}
			execute("start", "gh-6")
			execute("handoff", "gh-6", "--done", "Synthetic planned forward recovery work")
			execute("review", "gh-6")
			recoverWrite("approve", "gh-6", "--self-review", "--reason", "Synthetic fixture self-review")
			out = execute("show", "gh-6")
			if !strings.Contains(out, `"status":"closed"`) {
				t.Fatal(out)
			}
			if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
				t.Fatal("SQLite fallback", err)
			}
		})
	}
}

func TestGitHubJournalCLIGroupedBasicTransitionsAndPartialRecovery(t *testing.T) {
	for _, mode := range []string{"complete", "receipt-denied", "target-denied", "noop"} {
		t.Run(mode, func(t *testing.T) {
			dir, _, run := journalCLI(t)
			execute := func(args ...string) string {
				t.Helper()
				out, stderr, err := run(append(args, "--json")...)
				if err != nil {
					t.Fatal(args, out, stderr, err)
				}
				return out
			}
			execute("create", "Group first")
			execute("create", "Group second")
			execute("create", "Group third")
			if mode == "noop" {
				execute("start", "gh-6")
			}
			before, err := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
			if err != nil {
				t.Fatal(err)
			}
			if mode == "receipt-denied" || mode == "target-denied" {
				t.Setenv("TD_GH_JOURNAL_FAIL", mode)
				if mode == "target-denied" {
					t.Setenv("TD_SCHEDULE_FAIL_WRITE", "1")
				}
			}
			out, stderr, err := run("start", "gh-6", "gh-7", "gh-8", "--json")
			if (err != nil) != (mode == "receipt-denied" || mode == "target-denied") {
				t.Fatal(out, stderr, err)
			}
			t.Setenv("TD_GH_JOURNAL_FAIL", "")
			t.Setenv("TD_SCHEDULE_FAIL_WRITE", "")
			var rows []struct {
				ID, ActionType, Phase string
				Group                 *ghstore.ActionGroup `json:"group"`
			}
			last := execute("last", "--n", "1")
			if json.Unmarshal([]byte(last), &rows) != nil || len(rows) != 1 || rows[0].Group == nil {
				t.Fatal("member escaped group history", last)
			}
			rid := rows[0].ID
			native, err := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
			if err != nil {
				t.Fatal(err)
			}
			execute("undo", "--reconcile", rid)
			recovery := execute("undo", "--reconcile", rid, "--apply")
			if mode == "receipt-denied" || mode == "target-denied" {
				if !strings.Contains(recovery, `"group_receipt_saved":true`) || !strings.Contains(recovery, `"partial":true`) || !strings.Contains(recovery, `"target_write_verified":false`) {
					t.Fatal("partial result lost", recovery)
				}
			}
			execute("undo", "--reconcile", rid, "--apply")
			recovered, err := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
			if err != nil || !bytes.Equal(native, recovered) {
				t.Fatal("group recovery replayed target", err)
			}
			inverse := execute("undo")
			if !strings.Contains(inverse, `"group_members":`) || !strings.Contains(inverse, `"causal_undo_chain_eligible":false`) {
				t.Fatal(inverse)
			}
			restored, err := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
			if err != nil {
				t.Fatal(err)
			}
			// Compare content/state/labels while retaining genuine new server clocks.
			var b, a struct {
				Issues map[string]map[string]any `json:"issues"`
			}
			if json.Unmarshal(before, &b) != nil || json.Unmarshal(restored, &a) != nil {
				t.Fatal("native fixture invalid")
			}
			for _, id := range []string{"6", "7", "8"} {
				for _, field := range []string{"title", "body", "state", "labels"} {
					bv, _ := json.Marshal(b.Issues[id][field])
					av, _ := json.Marshal(a.Issues[id][field])
					if !bytes.Equal(bv, av) {
						t.Fatal("group inverse changed original contents", id, field, string(bv), string(av))
					}
				}
			}
			execute("undo", "--reconcile", rid, "--apply")
			confirmed, _ := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
			if !bytes.Equal(restored, confirmed) {
				t.Fatal("group inverse confirmation wrote target")
			}
			if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
				t.Fatal("SQLite fallback", err)
			}
		})
	}
}

func TestGitHubJournalCLIGroupInverseRecoveryContinuesWithoutReplayingCompletedMember(t *testing.T) {
	_, _, run := journalCLI(t)
	execute := func(args ...string) string {
		t.Helper()
		out, stderr, err := run(append(args, "--json")...)
		if err != nil {
			t.Fatal(args, out, stderr, err)
		}
		return out
	}
	for _, title := range []string{"One", "Two", "Three"} {
		execute("create", title)
	}
	execute("start", "gh-6", "gh-7", "gh-8")
	var rows []struct {
		ID, Phase, UndoOperationID string
		Group                      *ghstore.ActionGroup `json:"group"`
	}
	last := execute("last", "--n", "1")
	if json.Unmarshal([]byte(last), &rows) != nil || rows[0].Group == nil {
		t.Fatal(last)
	}
	rid := rows[0].ID
	t.Setenv("TD_GH_JOURNAL_FAIL", "undo-completion-denied")
	out, stderr, err := run("undo", "--json")
	if err == nil || !strings.Contains(out+stderr, "unverified") {
		t.Fatal(out, stderr, err)
	}
	t.Setenv("TD_GH_JOURNAL_FAIL", "")
	pending := execute("last", "--n", "1")
	// Inspect the group identity directly rather than inventing a new undo.
	var pendingValue []map[string]json.RawMessage
	if json.Unmarshal([]byte(pending), &pendingValue) != nil || string(pendingValue[0]["phase"]) != `"undo_prepared"` {
		t.Fatal(pending)
	}
	identity := string(pendingValue[0]["undo_operation_id"])
	before, _ := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
	execute("undo", "--reconcile", rid, "--apply")
	after, _ := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
	if !bytes.Equal(before, after) {
		t.Fatal("inverse recovery replayed target")
	}
	// The already restored last member retains its exact clock while the two
	// remaining members are explicitly undone in the next command invocation.
	var state struct{ Issues map[string]json.RawMessage }
	if json.Unmarshal(after, &state) != nil {
		t.Fatal("bad fixture")
	}
	restored8 := string(state.Issues["8"])
	done := execute("undo")
	if !strings.Contains(done, `"action":"undone"`) {
		t.Fatal(done)
	}
	latest, _ := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
	if json.Unmarshal(latest, &state) != nil || string(state.Issues["8"]) != restored8 {
		t.Fatal("completed inverse member was replayed")
	}
	confirmation := execute("undo", "--reconcile", rid, "--apply")
	var confirmed struct {
		Journal struct {
			UndoOperationID string `json:"undo_operation_id"`
		}
	}
	if json.Unmarshal([]byte(confirmation), &confirmed) != nil || `"`+confirmed.Journal.UndoOperationID+`"` != identity {
		t.Fatal("group undo identity changed", confirmation)
	}
}

func TestGitHubJournalCLIGroupRejectsDuplicateAndForeignMemberBeforeWrites(t *testing.T) {
	_, _, run := journalCLI(t)
	for _, title := range []string{"One", "Two"} {
		if out, stderr, err := run("create", title, "--json"); err != nil {
			t.Fatal(out, stderr, err)
		}
	}
	native, _ := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
	journals, _ := os.ReadFile(os.Getenv("TD_GH_FIXTURE_JOURNAL"))
	if out, stderr, err := run("start", "gh-6", "gh-6", "--json"); err == nil || !strings.Contains(out+stderr, "repeats") {
		t.Fatal(out, stderr, err)
	}
	next, _ := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
	history, _ := os.ReadFile(os.Getenv("TD_GH_FIXTURE_JOURNAL"))
	if !bytes.Equal(native, next) || !bytes.Equal(journals, history) {
		t.Fatal("duplicate preflight wrote")
	}
	if out, stderr, err := run("start", "gh-6", "gh-7", "--json"); err != nil {
		t.Fatal(out, stderr, err)
	}
	changed, _ := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
	var state map[string]any
	if json.Unmarshal(changed, &state) != nil {
		t.Fatal("fixture")
	}
	state["issues"].(map[string]any)["7"].(map[string]any)["title"] = "Concurrent same-clock foreign edit"
	data, _ := json.Marshal(state)
	if err := os.WriteFile(os.Getenv("TD_SCHEDULE_STATE"), data, 0600); err != nil {
		t.Fatal(err)
	}
	history, _ = os.ReadFile(os.Getenv("TD_GH_FIXTURE_JOURNAL"))
	if out, stderr, err := run("undo", "--json"); err == nil || !strings.Contains(out+stderr, "conflict") {
		t.Fatal(out, stderr, err)
	}
	final, _ := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
	latestHistory, _ := os.ReadFile(os.Getenv("TD_GH_FIXTURE_JOURNAL"))
	if !bytes.Equal(data, final) || !bytes.Equal(history, latestHistory) {
		t.Fatal("foreign member was partially undone")
	}
}

func TestGitHubJournalCLIGroupedBlockUnblockAndUnstart(t *testing.T) {
	for _, command := range []string{"block", "unblock", "unstart"} {
		t.Run(command, func(t *testing.T) {
			_, _, run := journalCLI(t)
			execute := func(args ...string) string {
				t.Helper()
				out, stderr, err := run(append(args, "--json")...)
				if err != nil {
					t.Fatal(args, out, stderr, err)
				}
				return out
			}
			execute("create", "One")
			execute("create", "Two")
			if command == "unblock" {
				execute("block", "gh-6")
				execute("block", "gh-7")
			}
			if command == "unstart" {
				execute("start", "gh-6")
				execute("start", "gh-7")
			}
			before, _ := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
			execute(command, "gh-6", "gh-7")
			out := execute("undo")
			if !strings.Contains(out, `"group_members":`) {
				t.Fatal(out)
			}
			after, _ := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
			var a, b struct{ Issues map[string]map[string]any }
			if json.Unmarshal(before, &a) != nil || json.Unmarshal(after, &b) != nil {
				t.Fatal("fixture")
			}
			for _, id := range []string{"6", "7"} {
				for _, field := range []string{"title", "body", "state", "labels"} {
					x, _ := json.Marshal(a.Issues[id][field])
					y, _ := json.Marshal(b.Issues[id][field])
					if !bytes.Equal(x, y) {
						t.Fatal("basic group inverse", command, id, field)
					}
				}
			}
		})
	}
}

func TestGitHubJournalCLIReviewCascadeGroupUndoAndHierarchyFence(t *testing.T) {
	for _, mode := range []string{"descendants", "parents", "new-child", "partial", "overlap", "overlap-parent"} {
		t.Run(mode, func(t *testing.T) {
			dir, _, run := journalCLI(t)
			execute := func(args ...string) string {
				t.Helper()
				out, stderr, err := run(append(args, "--json")...)
				if err != nil {
					t.Fatal(args, out, stderr, err)
				}
				return out
			}
			execute("create", "Root epic", "--type", "epic", "--minor")
			if mode == "overlap" || mode == "overlap-parent" {
				execute("create", "Child task", "--parent", "gh-6")
			} else {
				execute("create", "Child task", "--parent", "gh-6", "--minor")
			}
			execute("create", "Blocked sibling", "--parent", "gh-6")
			execute("block", "gh-8")
			root := "gh-6"
			if mode == "parents" || mode == "overlap-parent" {
				execute("delete", "gh-8", "--force")
			}
			if mode == "parents" {
				root = "gh-7"
			}
			before, _ := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
			if mode == "partial" {
				t.Setenv("TD_GH_JOURNAL_FAIL", "receipt-denied")
			}
			reviewArgs := []string{"review", root, "--json"}
			if mode == "overlap" {
				reviewArgs = []string{"review", root, "gh-7", "--minor", "--json"}
			}
			if mode == "overlap-parent" {
				reviewArgs = []string{"review", "gh-7", root, "--minor", "--json"}
			}
			out, stderr, err := run(reviewArgs...)
			if (err != nil) != (mode == "partial") {
				t.Fatal(out, stderr, err)
			}
			t.Setenv("TD_GH_JOURNAL_FAIL", "")
			if mode == "overlap" || mode == "overlap-parent" {
				var child struct{ Minor bool }
				out := execute("show", "gh-7")
				if json.Unmarshal([]byte(out), &child) != nil || !child.Minor {
					t.Fatal("explicit root minor option was lost", out)
				}
			}
			var rows []struct {
				ID    string
				Group *ghstore.ActionGroup
			}
			latest := execute("last", "--n", "1")
			if json.Unmarshal([]byte(latest), &rows) != nil || len(rows) != 1 || rows[0].Group == nil || rows[0].Group.Command != "review" {
				t.Fatal(latest)
			}
			rid := rows[0].ID
			if mode == "partial" {
				current, _ := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
				recovered := execute("undo", "--reconcile", rid, "--apply")
				after, _ := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
				if !bytes.Equal(current, after) || !strings.Contains(recovered, `"partial":true`) {
					t.Fatal(recovered)
				}
			}
			if mode == "new-child" {
				var external struct{ Issues map[string]map[string]any }
				if json.Unmarshal(before, &external) != nil {
					t.Fatal("fixture")
				}
				child := external.Issues["7"]
				child["number"] = 9
				child["title"] = "New externally added child"
				data, _ := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
				var live map[string]any
				_ = json.Unmarshal(data, &live)
				live["issues"].(map[string]any)["9"] = child
				data, _ = json.Marshal(live)
				if err := os.WriteFile(os.Getenv("TD_SCHEDULE_STATE"), data, 0600); err != nil {
					t.Fatal(err)
				}
				current, _ := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
				out, stderr, err := run("undo", "--json")
				if err == nil || !strings.Contains(out+stderr, "membership") {
					t.Fatal(stderr, err)
				}
				after, _ := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
				if !bytes.Equal(current, after) {
					t.Fatal("hierarchy conflict wrote targets")
				}
				return
			}
			inverse := execute("undo")
			if !strings.Contains(inverse, `"group_members":`) {
				t.Fatal(inverse)
			}
			after, _ := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
			var a, b struct{ Issues map[string]map[string]any }
			if json.Unmarshal(before, &a) != nil || json.Unmarshal(after, &b) != nil {
				t.Fatal("fixture")
			}
			for _, id := range []string{"6", "7", "8"} {
				for _, field := range []string{"body", "title", "state", "labels"} {
					x, _ := json.Marshal(a.Issues[id][field])
					y, _ := json.Marshal(b.Issues[id][field])
					if !bytes.Equal(x, y) {
						t.Fatal("cascade inverse differs", mode, id, field)
					}
				}
			}
			current, _ := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
			execute("undo", "--reconcile", rid, "--apply")
			after, _ = os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
			if !bytes.Equal(current, after) {
				t.Fatal("repeat changed clocks")
			}
			if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
				t.Fatal("SQLite", err)
			}
		})
	}
}

func TestGitHubJournalCLINativeClosureGroupAndRecovery(t *testing.T) {
	for _, mode := range []string{"cascade", "approve", "forward-partial", "inverse-partial", "inverse-native-partial", "reopen-batch", "late-dependent", "foreign-event-fence"} {
		t.Run(mode, func(t *testing.T) {
			dir, _, run := journalCLI(t)
			t.Setenv("TD_GH_FIXTURE_NATIVE_EVENTS", "1")
			execute := func(args ...string) string {
				t.Helper()
				out, stderr, err := run(append(args, "--json")...)
				if err != nil {
					t.Fatal(args, out, stderr, err)
				}
				return out
			}
			execute("create", "Aggregate epic", "--type", "epic")
			execute("create", "Minor leaf", "--parent", "gh-6", "--minor")
			execute("create", "Blocked dependent")
			execute("update", "gh-8", "--depends-on", "gh-6")
			execute("block", "gh-8")
			command := []string{"close", "gh-7", "--reason", "Synthetic fixture minor closure"}
			if mode == "approve" {
				execute("review", "gh-7")
				command = []string{"approve", "gh-7", "--self-review", "--reason", "Synthetic fixture minor review"}
			}
			if mode == "reopen-batch" {
				execute(command...)
				command = []string{"reopen", "gh-7", "gh-6", "--reason", "Synthetic fixture reopen"}
			}
			if mode == "inverse-native-partial" || mode == "foreign-event-fence" {
				execute("delete", "gh-8", "--force")
			}
			before, _ := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
			if mode == "forward-partial" {
				t.Setenv("TD_GH_JOURNAL_FAIL", "receipt-denied")
			}
			out, stderr, err := run(append(command, "--json")...)
			if (err != nil) != (mode == "forward-partial") {
				t.Fatal(out, stderr, err)
			}
			t.Setenv("TD_GH_JOURNAL_FAIL", "")
			var rows []struct {
				ID, Phase string
				Group     *ghstore.ActionGroup
			}
			latest := execute("last", "--n", "1")
			if json.Unmarshal([]byte(latest), &rows) != nil || len(rows) != 1 || rows[0].Group == nil {
				t.Fatal(latest)
			}
			rid := rows[0].ID
			if mode == "forward-partial" {
				current, _ := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
				result := execute("undo", "--reconcile", rid, "--apply")
				after, _ := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
				if !bytes.Equal(current, after) || !strings.Contains(result, `"partial":true`) {
					t.Fatal(result)
				}
			}
			if mode == "late-dependent" {
				data, _ := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
				var state map[string]any
				_ = json.Unmarshal(data, &state)
				issues := state["issues"].(map[string]any)
				raw, _ := json.Marshal(issues["8"])
				var child map[string]any
				_ = json.Unmarshal(raw, &child)
				child["number"] = 9
				child["title"] = "External new dependent"
				issues["9"] = child
				data, _ = json.Marshal(state)
				_ = os.WriteFile(os.Getenv("TD_SCHEDULE_STATE"), data, 0600)
				current, _ := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
				out, stderr, err := run("undo", "--json")
				if err == nil || !strings.Contains(out+stderr, "membership") {
					t.Fatal(out, stderr, err)
				}
				after, _ := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
				if !bytes.Equal(current, after) {
					t.Fatal("conflict mutated tasks")
				}
				return
			}
			if mode == "foreign-event-fence" {
				t.Setenv("TD_GH_GROUP_EVENT_TARGET", "7")
				current, _ := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
				out, stderr, err := run("undo", "--json")
				if err == nil || !strings.Contains(out+stderr, "event") {
					t.Fatal(out, stderr, err)
				}
				after, _ := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
				if !bytes.Equal(current, after) {
					t.Fatal("event collision allowed inverse target writes")
				}
				return
			}
			if mode == "inverse-partial" || mode == "inverse-native-partial" {
				t.Setenv("TD_GH_JOURNAL_FAIL", "undo-completion-denied")
				out, stderr, err := run("undo", "--json")
				if err == nil || !strings.Contains(out+stderr, "unverified") {
					t.Fatal(out, stderr, err)
				}
				t.Setenv("TD_GH_JOURNAL_FAIL", "")
				current, _ := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
				execute("undo", "--reconcile", rid, "--apply")
				after, _ := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
				if !bytes.Equal(current, after) {
					t.Fatal("inverse recovery replayed targets")
				}
			}
			inverse := execute("undo")
			if !strings.Contains(inverse, `"group_members":`) || !strings.Contains(inverse, `"causal_undo_chain_eligible":false`) {
				t.Fatal(inverse)
			}
			if mode == "inverse-native-partial" && !strings.Contains(inverse, `"target_write_verified":false`) {
				t.Fatal("observed native completion promoted", inverse)
			}
			after, _ := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
			var a, b struct{ Issues map[string]map[string]any }
			if json.Unmarshal(before, &a) != nil || json.Unmarshal(after, &b) != nil {
				t.Fatal("fixture")
			}
			for _, id := range []string{"6", "7", "8"} {
				for _, field := range []string{"title", "state", "labels"} {
					x, _ := json.Marshal(a.Issues[id][field])
					y, _ := json.Marshal(b.Issues[id][field])
					if !bytes.Equal(x, y) {
						t.Fatal("compensation differs", mode, id, field)
					}
				}
				body := b.Issues[id]["body"].(string)
				if id == "8" {
					if !sameFixtureTaskContentsWithoutAudit(t, body, a.Issues[id]["body"].(string)) {
						t.Fatal("dependent task fields not restored")
					}
				} else if mode != "forward-partial" || id == "7" {
					if !strings.Contains(body, `"action":"undo_group"`) {
						t.Fatal("native audit missing", id, body)
					}
				}
			}
			current, _ := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
			execute("undo", "--reconcile", rid, "--apply")
			after, _ = os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
			if !bytes.Equal(current, after) {
				t.Fatal("confirmation replayed tasks")
			}
			if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
				t.Fatal("SQLite", err)
			}
		})
	}
}

package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/marcus/td/internal/ghstore"
)

// An import command owns its actual destination mutations, independently of
// the passive source history it preserves. One undo compensates the command;
// it must not accidentally select an older unrelated current-session action.
func TestGitHubJournalCLIImportWholeCommand(t *testing.T) {
	run, execute := importJournalCLI(t)
	execute("create", "Unrelated task remains", "--minor")
	execute("log", "gh-6", "Original unrelated activity")
	before := relationFixtureBytes(t, "TD_SCHEDULE_STATE")
	input := filepath.Join(t.TempDir(), "import.json")
	if err := os.WriteFile(input, []byte(`[
		{"issue":{"id":"td-parent","title":"Imported parent","type":"epic","minor":true}},
		{"issue":{"id":"td-child","title":"Imported child","parent_id":"td-parent","minor":true},"dependencies":["td-parent"],"logs":[{"id":"original-log","message":"Preserved source history","type":"progress"}]}
	]`), 0600); err != nil {
		t.Fatal(err)
	}
	var result ghstore.ImportResult
	if out := execute("import", input, "--source", "command-fixture"); json.Unmarshal([]byte(out), &result) != nil || result.Completed != 2 || result.Created != 2 {
		t.Fatal("import did not apply both sources", out)
	}
	var history []struct {
		ID            string
		Action        string `json:"action_type"`
		Phase         string
		ImportCommand json.RawMessage `json:"import_command"`
	}
	if out := execute("last", "--n", "1"); json.Unmarshal([]byte(out), &history) != nil || len(history) != 1 || history[0].Action != "import" || history[0].Phase != "applied" || len(history[0].ImportCommand) == 0 {
		t.Fatal("import is missing a current-session command; undo would select an unrelated earlier action", out)
	}
	rootID := history[0].ID
	native, journals := relationFixtureBytes(t, "TD_SCHEDULE_STATE"), relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")
	execute("import", input, "--source", "command-fixture")
	if !bytes.Equal(native, relationFixtureBytes(t, "TD_SCHEDULE_STATE")) || !bytes.Equal(journals, relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")) {
		t.Fatal("completed import repeated target or command writes")
	}
	execute("undo")
	for _, row := range result.Plan.Rows {
		out, stderr, err := run("show", row.TargetID, "--json")
		if err == nil || !strings.Contains(out+stderr, "deleted") {
			t.Fatal("import inverse did not logically delete its real allocation", row.TargetID, out, stderr, err)
		}
	}
	var original, imported, restored struct {
		Issues   map[string]map[string]any
		Comments []map[string]any
	}
	if json.Unmarshal(before, &original) != nil || json.Unmarshal(native, &imported) != nil || json.Unmarshal(relationFixtureBytes(t, "TD_SCHEDULE_STATE"), &restored) != nil {
		t.Fatal("invalid native fixture")
	}
	if !reflect.DeepEqual(original.Issues["6"], restored.Issues["6"]) || !reflect.DeepEqual(imported.Comments, restored.Comments) {
		t.Fatal("import inverse changed unrelated task or removed native provenance/history")
	}
	for _, row := range result.Plan.Rows {
		id := strings.TrimPrefix(row.TargetID, "gh-")
		for _, key := range []string{"number", "title", "state", "created_at"} {
			if !reflect.DeepEqual(imported.Issues[id][key], restored.Issues[id][key]) {
				t.Fatal("allocation compensation replaced native identity/state", row.TargetID, key)
			}
		}
	}
	native, journals = relationFixtureBytes(t, "TD_SCHEDULE_STATE"), relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")
	execute("undo", "--reconcile", rootID, "--apply")
	if !bytes.Equal(native, relationFixtureBytes(t, "TD_SCHEDULE_STATE")) || !bytes.Equal(journals, relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")) {
		t.Fatal("completed import compensation repeated a write")
	}
	// A verified whole compensation permits a new generation, retaining the
	// native source mapping and prior receipts rather than allocating duplicates.
	var reimported ghstore.ImportResult
	if out := execute("import", input, "--source", "command-fixture"); json.Unmarshal([]byte(out), &reimported) != nil || reimported.Completed != 2 || reimported.Created != 0 {
		t.Fatal("import after undo did not reuse its original allocations", out)
	}
	for n, row := range result.Plan.Rows {
		if reimported.Plan.Rows[n].TargetID != row.TargetID {
			t.Fatal("new generation changed destination identity", row, reimported.Plan.Rows[n])
		}
		if out, stderr, err := run("show", row.TargetID, "--json"); err != nil {
			t.Fatal("new generation did not restore its destination", out, stderr, err)
		}
	}
	if out := execute("last", "--n", "1"); json.Unmarshal([]byte(out), &history) != nil || len(history) != 1 || history[0].Action != "import" || history[0].ID == rootID {
		t.Fatal("reimport reused the compensated command instead of a new generation", out)
	}
	execute("undo")
	for _, row := range result.Plan.Rows {
		if out, stderr, err := run("show", row.TargetID, "--json"); err == nil || !strings.Contains(out+stderr, "deleted") {
			t.Fatal("new generation undo did not restore its compensated baseline", row.TargetID, out, stderr, err)
		}
	}
}

func installImportInverseReadFailure(t *testing.T) {
	t.Helper()
	path, err := exec.LookPath("gh")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	script := string(data)
	for before, after := range map[string]string{
		"def fixture_tick(value):": `inverse_read_mode=os.environ.get('TD_GH_IMPORT_UNDO_READ_FAIL')
if inverse_read_mode and journal.get('inverse_root_writes',0)>=2 and not journal.get('inverse_read_failed') and method=='GET' and ((inverse_read_mode=='snapshot' and path.startswith('repos/owner/repo/issues?')) or (inverse_read_mode!='snapshot' and path=='repos/owner/repo/issues/6')):
 journal['inverse_read_failed']=True;journal_file.write_text(json.dumps(journal));sys.stderr.write('fixture pre-PATCH GET failed');sys.exit(1)
def fixture_tick(value):`,
		"   if os.environ.get('TD_GH_JOURNAL_FAIL')=='receipt-lost':": `   if os.environ.get('TD_GH_IMPORT_UNDO_READ_FAIL'):
    journal['inverse_root_writes']=journal.get('inverse_root_writes',0)+1;journal_file.write_text(json.dumps(journal))
   if os.environ.get('TD_GH_JOURNAL_FAIL')=='receipt-lost':`,
		"   data.update(payload);": `   if inverse_read_mode=='cancel-denied' and journal.get('inverse_read_failed'):sys.stderr.write('fixture cancellation receipt denied');sys.exit(1)
   data.update(payload);`,
	} {
		if !strings.Contains(script, before) {
			t.Fatal("missing inverse read failure seam", before)
		}
		script = strings.Replace(script, before, after, 1)
	}
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
}

func importLastCommand(t *testing.T, execute func(...string) string) ghstore.ActionJournalRecord {
	t.Helper()
	var history []ghstore.ActionJournalRecord
	if out := execute("last", "--n", "1"); json.Unmarshal([]byte(out), &history) != nil || len(history) != 1 || history[0].ImportCommand == nil {
		t.Fatal("missing import command", out)
	}
	return history[0]
}

func TestGitHubJournalCLIImportUndoPreWriteReadFailureCanResume(t *testing.T) {
	for _, mode := range []string{"target", "snapshot", "cancel-denied", "patch-denied", "patch-lost"} {
		t.Run(mode, func(t *testing.T) {
			run, execute := importJournalCLI(t)
			file := filepath.Join(t.TempDir(), "input.json")
			if err := os.WriteFile(file, []byte(`[{"issue":{"id":"td-one","title":"Undo read failure","minor":true}}]`), 0600); err != nil {
				t.Fatal(err)
			}
			execute("import", file, "--source", "inverse-read-fixture")
			installImportInverseReadFailure(t)
			before := importFixtureStateBytes(t)
			message := "fixture pre-PATCH GET failed"
			switch mode {
			case "patch-denied":
				t.Setenv("TD_SCHEDULE_FAIL_WRITE", "1")
				message = "HTTP 403 write denied"
			case "patch-lost":
				t.Setenv("TD_GH_JOURNAL_FAIL", "target-lost")
				message = "connection lost after target"
			default:
				t.Setenv("TD_GH_IMPORT_UNDO_READ_FAIL", mode)
			}
			out, stderr, err := run("undo", "--json")
			if err == nil || !strings.Contains(out+stderr, message) {
				t.Fatal("original failure was hidden", out, stderr, err)
			}
			if mode == "cancel-denied" && !strings.Contains(out+stderr, "fixture cancellation receipt denied") {
				t.Fatal("cancellation failure was hidden", out, stderr)
			}
			if mode != "patch-lost" && !bytes.Equal(before, importFixtureStateBytes(t)) {
				t.Fatal("pre-write failure changed an import target")
			}
			t.Setenv("TD_GH_IMPORT_UNDO_READ_FAIL", "")
			t.Setenv("TD_SCHEDULE_FAIL_WRITE", "")
			t.Setenv("TD_GH_JOURNAL_FAIL", "")
			root := importLastCommand(t, execute)
			if mode == "target" || mode == "snapshot" {
				if root.ImportInverse.Started != 0 || len(root.ImportInverse.Canceled) != 1 || root.ImportInverse.Canceled[0].Stage != mode {
					t.Fatal("missing durable pre-write cancellation", root.ImportInverse)
				}
				execute("undo")
				done := importLastCommand(t, execute)
				if done.Phase != "undone" || len(done.ImportInverse.Canceled) != 1 {
					t.Fatal("resume lost cancellation history")
				}
			} else {
				state, journals := importFixtureStateBytes(t), relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")
				if out, stderr, err := run("undo", "--json"); err == nil || !strings.Contains(out+stderr, "uncertain step") {
					t.Fatal("uncertain inverse was replayed", out, stderr, err)
				}
				if !bytes.Equal(state, importFixtureStateBytes(t)) || !bytes.Equal(journals, relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")) {
					t.Fatal("refused undo changed remote state")
				}
				originalSession := os.Getenv("TD_SESSION_ID")
				t.Setenv("TD_SESSION_ID", "inverse-observer")
				var observation ghstore.ActionReconcileResult
				if out := execute("undo", "--reconcile", root.ID, "--apply"); json.Unmarshal([]byte(out), &observation) != nil || observation.TargetWriteReplayed {
					t.Fatal("invalid receipt-only recovery", out)
				}
				if !bytes.Equal(state, importFixtureStateBytes(t)) {
					t.Fatal("reconcile replayed target write")
				}
				t.Setenv("TD_SESSION_ID", originalSession)
				if mode != "patch-lost" {
					if !observation.Partial || observation.Applied || !bytes.Equal(journals, relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")) {
						t.Fatal("unchanged target authorized clearing uncertainty", observation)
					}
					return
				}
				if observation.Journal.Phase != "undone" || observation.Journal.ImportInverse.Result.Proof != "observed" {
					t.Fatal("observed inverse was upgraded to verified", observation)
				}
				execute("undo", "--reconcile", root.ID, "--apply")
			}
			if out, stderr, err := run("show", "gh-6", "--json"); err == nil || !strings.Contains(out+stderr, "deleted") {
				t.Fatal("undo did not complete", out, stderr, err)
			}
		})
	}
}

func TestGitHubJournalCLIImportCompensationRejectsLaterChanges(t *testing.T) {
	for _, mode := range []string{"fields", "comments", "events", "partial", "newer-receipt"} {
		t.Run(mode, func(t *testing.T) {
			run, execute := importJournalCLI(t)
			file := filepath.Join(t.TempDir(), "input.json")
			if err := os.WriteFile(file, []byte(`[{"issue":{"id":"td-one","title":"Compensation conflict","minor":true}}]`), 0600); err != nil {
				t.Fatal(err)
			}
			execute("import", file, "--source", "compensation-conflict")
			if mode == "partial" {
				t.Setenv("TD_GH_JOURNAL_FAIL", "target-lost")
				if out, stderr, err := run("undo", "--json"); err == nil || !strings.Contains(out+stderr, "connection lost after target") {
					t.Fatal("missing partial inverse", out, stderr, err)
				}
				t.Setenv("TD_GH_JOURNAL_FAIL", "")
			} else {
				execute("undo")
			}
			if mode == "newer-receipt" {
				execute("import", file, "--source", "compensation-conflict")
			}
			var state struct {
				Issues        map[string]map[string]any `json:"issues"`
				Comments      []map[string]any          `json:"comments"`
				NextCommentID int                       `json:"next_comment_id"`
			}
			if err := json.Unmarshal(importFixtureStateBytes(t), &state); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "fields", "newer-receipt":
				state.Issues["6"]["title"] = "Changed by another writer"
			case "comments":
				clock := state.Issues["6"]["updated_at"]
				state.Comments = append(state.Comments, map[string]any{"id": 999999, "body": "External comment after inverse", "issue_url": "https://api.github.com/repos/owner/repo/issues/6", "created_at": clock, "updated_at": clock, "user": map[string]string{"login": "external-author"}})
			case "events":
				var journal map[string]any
				if err := json.Unmarshal(relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL"), &journal); err != nil {
					t.Fatal(err)
				}
				journal["native_events"].(map[string]any)["6"] = []map[string]any{{"id": 100, "event": "closed"}, {"id": 101, "event": "reopened"}}
				data, err := json.Marshal(journal)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(os.Getenv("TD_GH_FIXTURE_JOURNAL"), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			data, err := json.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(os.Getenv("TD_SCHEDULE_STATE"), data, 0600); err != nil {
				t.Fatal(err)
			}
			before, journals := importFixtureStateBytes(t), relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")
			if out, stderr, err := run("import", file, "--source", "compensation-conflict", "--force", "--json"); err == nil {
				t.Fatal("unsafe compensated reimport was accepted", out, stderr)
			}
			if !bytes.Equal(before, importFixtureStateBytes(t)) || !bytes.Equal(journals, relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")) {
				t.Fatal("refused reimport changed native data or command history")
			}
		})
	}
}

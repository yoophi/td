package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marcus/td/internal/ghstore"
)

func importJournalCLI(t *testing.T) (func(...string) (string, string, error), func(...string) string) {
	t.Helper()
	run, execute := workflowJournalCLI(t)
	configureImportJournalFixture(t)
	return run, execute
}

func configureImportJournalFixture(t *testing.T) {
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
	replacements := map[string]string{
		"import datetime,io": `import datetime,io
# Match native clocks across the task and journal fixture stores.
def import_fixture_clock():
 times=['2026-10-09T00:00:00Z']
 for c in journal['comments']:times.extend([c['created_at'],c['updated_at']])
 state_path=pathlib.Path(os.environ['TD_SCHEDULE_STATE'])
 if state_path.exists():times.extend(i['updated_at'] for i in json.loads(state_path.read_text())['issues'].values())
 return fixture_tick(max(times))`,
		"if path=='repos/owner/repo/issues/comments?per_page=100':":                                                    "if path in ('repos/owner/repo/issues/comments?per_page=100','repos/owner/repo/issues/999/comments?per_page=100'):",
		"('2026-10-09T00:00:00Z' if not journal['comments'] else fixture_tick(journal['comments'][-1]['created_at']))": "import_fixture_clock()",
		"data.update(payload);data['updated_at']=fixture_tick(data['updated_at'])":                                     "data.update(payload);data['updated_at']=import_fixture_clock()",
		"'updated_at':'2026-10-09T00:00:00Z','user'":                                                                   "'created_at':'2026-10-09T00:00:00Z','updated_at':'2026-10-09T00:00:00Z','user'",
		"if os.environ.get('TD_GH_JOURNAL_FAIL')=='intent-lost':":                                                      "if os.environ.get('TD_GH_IMPORT_FAIL')=='root-lost' and '<!-- td:action:v34' in body:sys.stderr.write('connection lost after import root');sys.exit(1)\n if os.environ.get('TD_GH_JOURNAL_FAIL')=='intent-lost':",
		"data.update(patch);issues[n]=data;f.write_text(json.dumps(state))":                                            "data.update(patch);data['created_at']=import_fixture_clock();data['updated_at']=data['created_at'];issues[n]=data;f.write_text(json.dumps(state))\n if os.environ.get('TD_GH_IMPORT_FAIL')=='allocation-lost':sys.stderr.write('connection lost after import allocation');sys.exit(1)",
	}
	for before, after := range replacements {
		if !strings.Contains(script, before) {
			t.Fatal("import fixture seam missing", before)
		}
		script = strings.Replace(script, before, after, 1)
	}
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
}

func TestGitHubJournalCLIImportForwardAndResume(t *testing.T) {
	for _, failure := range []string{"none", "root-lost", "allocation-lost", "target-lost", "receipt-denied", "receipt-lost"} {
		t.Run(failure, func(t *testing.T) {
			run, execute := importJournalCLI(t)
			input := filepath.Join(t.TempDir(), "import.json")
			if err := os.WriteFile(input, []byte(`[{"issue":{"id":"td-parent","title":"Import parent","type":"epic","minor":true}},{"issue":{"id":"td-child","title":"Import child","parent_id":"td-parent","minor":true},"dependencies":["td-parent"]}]`), 0600); err != nil {
				t.Fatal(err)
			}
			if failure == "root-lost" || failure == "allocation-lost" {
				t.Setenv("TD_GH_IMPORT_FAIL", failure)
			} else if failure != "none" {
				t.Setenv("TD_GH_JOURNAL_FAIL", failure)
			}
			out, stderr, err := run("import", input, "--source", "forward-fixture", "--json")
			if (err != nil) != (failure != "none") {
				t.Fatal("unexpected initial result", failure, out, stderr, err)
			}
			t.Setenv("TD_GH_IMPORT_FAIL", "")
			t.Setenv("TD_GH_JOURNAL_FAIL", "")
			var before []ghstore.ActionJournalRecord
			if out := execute("last", "--n", "20"); json.Unmarshal([]byte(out), &before) != nil || len(before) != 1 || before[0].ImportCommand == nil {
				t.Fatal("missing single original command", out)
			}
			if failure != "none" && failure != "receipt-lost" {
				if before[0].Phase != "prepared" {
					t.Fatal("uncertain command was reported complete", before[0].Phase)
				}
				originalSession := os.Getenv("TD_SESSION_ID")
				t.Setenv("TD_SESSION_ID", "import-observer")
				state, journals := importFixtureStateBytes(t), relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")
				if out, stderr, err := run("import", input, "--source", "forward-fixture", "--json"); err == nil || !strings.Contains(out+stderr, "belongs to session") {
					t.Fatal("another session took ownership", out, stderr, err)
				}
				var observation ghstore.ActionReconcileResult
				if out := execute("undo", "--reconcile", before[0].ID); json.Unmarshal([]byte(out), &observation) != nil || observation.TargetWriteReplayed {
					t.Fatal("invalid receipt-only preview", out)
				}
				if !bytes.Equal(state, importFixtureStateBytes(t)) || !bytes.Equal(journals, relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")) {
					t.Fatal("observation replayed a write")
				}
				if out := execute("undo", "--reconcile", before[0].ID, "--apply"); json.Unmarshal([]byte(out), &observation) != nil || observation.TargetWriteReplayed {
					t.Fatal("invalid receipt-only apply", out)
				}
				if !bytes.Equal(state, importFixtureStateBytes(t)) {
					t.Fatal("receipt-only apply replayed target writes")
				}
				if failure == "receipt-denied" {
					if !observation.Applied || observation.Journal.ImportForward == nil || observation.Journal.ImportForward.Observation.SessionID == before[0].SessionID {
						t.Fatal("completed observation lost actual observer", observation)
					}
				} else if !observation.Partial || !bytes.Equal(journals, relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")) {
					t.Fatal("partial observation changed original root", observation)
				}
				t.Setenv("TD_SESSION_ID", originalSession)
			}
			execute("import", input, "--source", "forward-fixture")
			var after []ghstore.ActionJournalRecord
			if out := execute("last", "--n", "20"); json.Unmarshal([]byte(out), &after) != nil || len(after) != 1 || after[0].ID != before[0].ID || after[0].Phase != "applied" || after[0].ImportForward == nil {
				t.Fatal("resume lost original command or completion", out)
			}
			expectedProof := "verified"
			if failure != "none" && failure != "receipt-lost" {
				expectedProof = "observed"
			}
			if after[0].ImportForward.Observation.Proof != expectedProof {
				t.Fatal("uncertain result upgraded to verified", after[0].ImportForward.Observation)
			}
			state, journals := importFixtureStateBytes(t), relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")
			execute("import", input, "--source", "forward-fixture")
			execute("undo", "--reconcile", after[0].ID, "--apply")
			if !bytes.Equal(state, importFixtureStateBytes(t)) || !bytes.Equal(journals, relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")) {
				t.Fatal("completed repeat changed native state/history")
			}
			var native struct{ Issues map[string]json.RawMessage }
			if json.Unmarshal(state, &native) != nil || len(native.Issues) != 7 {
				t.Fatal("resume duplicated task allocations", len(native.Issues))
			}
		})
	}
}

func importFixtureStateBytes(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestGitHubJournalCLIImportChangedSourceStartsNewCommand(t *testing.T) {
	_, execute := importJournalCLI(t)
	file := filepath.Join(t.TempDir(), "input.json")
	original := `[{"issue":{"id":"td-one","title":"Original input","minor":true}}]`
	if err := os.WriteFile(file, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	execute("import", file, "--source", "generation")
	var prior []ghstore.ActionJournalRecord
	if out := execute("last", "--n", "1"); json.Unmarshal([]byte(out), &prior) != nil || len(prior) != 1 {
		t.Fatal(out)
	}
	if err := os.WriteFile(file, []byte(strings.Replace(original, "Original input", "Changed input", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	var result ghstore.ImportResult
	if out := execute("import", file, "--source", "generation", "--force"); json.Unmarshal([]byte(out), &result) != nil || result.Created != 0 || result.Completed != 1 || result.Plan.Rows[0].TargetID != "gh-6" {
		t.Fatal("changed input did not preserve destination", out)
	}
	var after []ghstore.ActionJournalRecord
	if out := execute("last", "--n", "2"); json.Unmarshal([]byte(out), &after) != nil || len(after) != 2 || after[0].ID == prior[0].ID || after[1].ID != prior[0].ID || after[0].Phase != "applied" {
		t.Fatal("changed input did not retain both original commands", out)
	}
	if after[1].ImportCommand.Input != prior[0].ImportCommand.Input || after[0].ImportCommand.Input == prior[0].ImportCommand.Input {
		t.Fatal("new generation replaced old input")
	}
	state, journals := importFixtureStateBytes(t), relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")
	execute("import", file, "--source", "generation", "--force")
	if !bytes.Equal(state, importFixtureStateBytes(t)) || !bytes.Equal(journals, relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")) {
		t.Fatal("new generation no-op repeated writes")
	}
}

func TestGitHubJournalCLIImportNativeForceRestoreAfterLegacyAllocation(t *testing.T) {
	run, execute := importJournalCLI(t)
	file := filepath.Join(t.TempDir(), "input.json")
	if err := os.WriteFile(file, []byte(`[{"issue":{"id":"td-one","title":"Exported title","minor":true}}]`), 0600); err != nil {
		t.Fatal(err)
	}
	execute("import", file, "--source", "legacy-project")
	var allocated []ghstore.ActionJournalRecord
	if out := execute("last", "--n", "1"); json.Unmarshal([]byte(out), &allocated) != nil || len(allocated) != 1 {
		t.Fatal(out)
	}
	exported, err := ghstore.ParseImportJSON([]byte(execute("export", "--all")))
	if err != nil {
		t.Fatal(err)
	}
	var selected []ghstore.ImportItem
	for _, item := range exported {
		if item.Issue.ID == "gh-6" {
			selected = append(selected, item)
		}
	}
	if len(selected) != 1 || selected[0].GitHub == nil {
		t.Fatal("missing native exported allocation")
	}
	data, err := json.Marshal(selected)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	execute("update", "gh-6", "--title", "Edited after export")
	var result ghstore.ImportResult
	if out := execute("import", file, "--force"); json.Unmarshal([]byte(out), &result) != nil || result.Created != 0 || result.Completed != 1 || result.Plan.Rows[0].TargetID != "gh-6" {
		t.Fatal("native restore did not reuse the original destination", out)
	}
	var restored []ghstore.ActionJournalRecord
	if out := execute("last", "--n", "1"); json.Unmarshal([]byte(out), &restored) != nil || len(restored) != 1 || restored[0].ImportForward == nil {
		t.Fatal("native restore did not record its command", out)
	}
	root := restored[0]
	if root.ID == allocated[0].ID || root.ImportCommand.Members[0].Before.Issue.Title != "Edited after export" || root.ImportForward.Targets[0].Issue.Title != "Exported title" {
		t.Fatal("restore replaced original command or lost current before-state")
	}
	if !strings.Contains(root.ImportForward.Targets[0].Issue.Body, allocated[0].ID) {
		t.Fatal("native restore replaced the original allocation owner")
	}
	state, journals := importFixtureStateBytes(t), relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")
	execute("import", file, "--force")
	if !bytes.Equal(state, importFixtureStateBytes(t)) || !bytes.Equal(journals, relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")) {
		t.Fatal("completed native restore repeated writes")
	}
	// A receipt for this native source now exists. Its conflict protection is
	// distinct from the allocation owner and must survive the compatibility fix.
	execute("update", "gh-6", "--title", "Edit after native completion")
	state, journals = importFixtureStateBytes(t), relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")
	if out, stderr, err := run("import", file, "--force", "--json"); err == nil || !strings.Contains(out+stderr, "changed after completion") {
		t.Fatal("force bypassed this source's completed receipt", out, stderr, err)
	}
	if !bytes.Equal(state, importFixtureStateBytes(t)) || !bytes.Equal(journals, relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")) {
		t.Fatal("failed restore changed targets or journal")
	}
}

func TestGitHubJournalCLIImportNativeRestoreCannotTakeOverPendingAllocation(t *testing.T) {
	run, execute := importJournalCLI(t)
	file := filepath.Join(t.TempDir(), "input.json")
	if err := os.WriteFile(file, []byte(`[{"issue":{"id":"td-one","title":"Pending allocation","minor":true}}]`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TD_GH_IMPORT_FAIL", "allocation-lost")
	if out, stderr, err := run("import", file, "--source", "pending-project", "--json"); err == nil || !strings.Contains(out+stderr, "connection lost after import allocation") {
		t.Fatal("fixture did not leave a pending allocation", out, stderr, err)
	}
	t.Setenv("TD_GH_IMPORT_FAIL", "")
	items, err := ghstore.ParseImportJSON([]byte(execute("export", "--all")))
	if err != nil {
		t.Fatal(err)
	}
	var selected []ghstore.ImportItem
	for _, item := range items {
		if item.Issue.ID == "gh-6" {
			selected = append(selected, item)
		}
	}
	if len(selected) != 1 || selected[0].GitHub == nil {
		t.Fatal("missing native pending allocation")
	}
	data, err := json.Marshal(selected)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TD_SESSION_ID", "native-takeover-attempt")
	state, journals := importFixtureStateBytes(t), relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")
	if out, stderr, err := run("import", file, "--force", "--json"); err == nil || !strings.Contains(out+stderr, "pending import") {
		t.Fatal("native source bypassed pending import ownership", out, stderr, err)
	}
	if !bytes.Equal(state, importFixtureStateBytes(t)) || !bytes.Equal(journals, relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")) {
		t.Fatal("native takeover attempt changed targets or command history")
	}
}

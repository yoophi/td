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

func installImportInverseReceiptFailure(t *testing.T) {
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
		"   data.update(payload);": `   inverse_receipt_mode=os.environ.get('TD_GH_IMPORT_INVERSE_RECEIPT_FAIL')
   import_marker='<!-- td:action:v34\n';import_body=payload.get('body','');import_manifest=None
   if import_marker in import_body:import_manifest=json.loads(import_body.split(import_marker,1)[1].rsplit('\n-->',1)[0])
   import_phase=import_manifest.get('journal',{}).get('phase') if import_manifest else None
   if inverse_receipt_mode=='completion-denied' and import_phase=='undone':sys.stderr.write('HTTP 403 import inverse completion receipt denied');sys.exit(1)
   data.update(payload);`,
		"   if os.environ.get('TD_GH_JOURNAL_FAIL')=='receipt-lost':": `   if inverse_receipt_mode=='step-lost' and import_phase=='undo_prepared' and import_manifest.get('inverse_results',0)==1:sys.stderr.write('connection lost after import inverse result receipt');sys.exit(1)
   if os.environ.get('TD_GH_JOURNAL_FAIL')=='receipt-lost':`,
	} {
		if !strings.Contains(script, before) {
			t.Fatal("missing inverse result failure seam", before)
		}
		script = strings.Replace(script, before, after, 1)
	}
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
}

func TestGitHubJournalCLIImportInverseReceiptRecovery(t *testing.T) {
	for _, mode := range []string{"step-lost", "completion-denied", "external-change"} {
		t.Run(mode, func(t *testing.T) {
			run, execute := importJournalCLI(t)
			file := filepath.Join(t.TempDir(), "sources.json")
			if err := os.WriteFile(file, []byte(`[{"issue":{"id":"td-first","title":"First receipt source","minor":true}},{"issue":{"id":"td-second","title":"Second receipt source","minor":true}}]`), 0600); err != nil {
				t.Fatal(err)
			}
			execute("import", file, "--source", "inverse-receipt-fixture")
			forward := importLastCommand(t, execute)
			before := readImportNativeFixture(t)
			patches := recordImportFixturePatches(t)
			installImportInverseReceiptFailure(t)
			failureMode := mode
			if mode == "external-change" {
				failureMode = "step-lost"
			}
			t.Setenv("TD_GH_IMPORT_INVERSE_RECEIPT_FAIL", failureMode)
			message := "connection lost after import inverse result receipt"
			count := 1
			if mode == "completion-denied" {
				message, count = "HTTP 403 import inverse completion receipt denied", 2
			}
			if out, stderr, err := run("undo", "--json"); err == nil || !strings.Contains(out+stderr, message) {
				t.Fatal("inverse receipt failure was hidden or reported successful", out, stderr, err)
			}
			t.Setenv("TD_GH_IMPORT_INVERSE_RECEIPT_FAIL", "")
			root := importLastCommand(t, execute)
			if root.ID != forward.ID || root.Phase != "undo_prepared" || root.ImportInverse == nil || len(root.ImportInverse.Results) != count || len(patches()) != count {
				t.Fatal("receipt failure lost known results, repeated a PATCH or claimed completion", root.Phase, len(patches()))
			}
			if mode == "external-change" {
				var external map[string]any
				if err := json.Unmarshal(importFixtureStateBytes(t), &external); err != nil {
					t.Fatal(err)
				}
				external["issues"].(map[string]any)["7"].(map[string]any)["title"] = "External edit must survive"
				data, err := json.Marshal(external)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(os.Getenv("TD_SCHEDULE_STATE"), data, 0600); err != nil {
					t.Fatal(err)
				}
				state, journals := importFixtureStateBytes(t), relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")
				for _, args := range [][]string{{"undo", "--json"}, {"undo", "--reconcile", root.ID, "--apply", "--json"}} {
					if out, stderr, err := run(args...); err == nil || !strings.Contains(out+stderr, "conflict") {
						t.Fatal("external edit authorized compensation or result adoption", args, out, stderr, err)
					}
					if !bytes.Equal(state, importFixtureStateBytes(t)) || !bytes.Equal(journals, relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")) || len(patches()) != 1 {
						t.Fatal("conflict overwrote the external edit or changed command evidence")
					}
				}
				return
			}
			state := importFixtureStateBytes(t)
			var observation ghstore.ActionReconcileResult
			if out := execute("undo", "--reconcile", root.ID, "--apply"); json.Unmarshal([]byte(out), &observation) != nil || observation.TargetWriteReplayed {
				t.Fatal("receipt-only recovery replayed target changes", out)
			}
			if !bytes.Equal(state, importFixtureStateBytes(t)) || len(patches()) != count {
				t.Fatal("reconciliation applied unfinished target changes")
			}
			if mode == "step-lost" {
				if !observation.Partial {
					t.Fatal("partially undone import was reported complete")
				}
				execute("undo")
			} else if !observation.Applied || observation.Partial || observation.Journal.Phase != "undone" || observation.Journal.ImportInverse.Result.Proof != "observed" {
				t.Fatal("completion recovery lost actual observed attribution")
			}
			done := importLastCommand(t, execute)
			if done.Phase != "undone" || !reflect.DeepEqual(done.ImportCommand, forward.ImportCommand) || !reflect.DeepEqual(done.ImportForward, forward.ImportForward) {
				t.Fatal("inverse recovery replaced original evidence")
			}
			counts := map[string]int{}
			for _, patch := range patches() {
				counts[patch["id"].(string)]++
			}
			if !reflect.DeepEqual(counts, map[string]int{"6": 1, "7": 1}) {
				t.Fatal("recovered undo repeated or omitted target changes", counts)
			}
			after := readImportNativeFixture(t)
			if len(before.Issues) != len(after.Issues) || !reflect.DeepEqual(before.Comments, after.Comments) {
				t.Fatal("inverse recovery changed native history")
			}
			for _, id := range []string{"gh-6", "gh-7"} {
				if out, stderr, err := run("show", id, "--json"); err == nil || !strings.Contains(out+stderr, "deleted") {
					t.Fatal("recovered inverse did not compensate the actual allocation", id, out, stderr, err)
				}
				number := strings.TrimPrefix(id, "gh-")
				for _, field := range []string{"number", "created_at", "user", "url", "state"} {
					if !reflect.DeepEqual(before.Issues[number][field], after.Issues[number][field]) {
						t.Fatal("recovery changed native identity or authorship", id, field)
					}
				}
			}
			state, journals := importFixtureStateBytes(t), relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")
			execute("undo", "--reconcile", root.ID, "--apply")
			if !bytes.Equal(state, importFixtureStateBytes(t)) || !bytes.Equal(journals, relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")) || len(patches()) != 2 {
				t.Fatal("completed inverse recovery repeated native or journal writes")
			}
		})
	}
}

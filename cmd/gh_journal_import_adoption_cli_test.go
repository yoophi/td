package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/marcus/td/internal/ghstore"
)

func TestGitHubJournalCLIImportAdoptionCompletionRepeatAndUndo(t *testing.T) {
	for _, all := range []bool{false, true} {
		t.Run(map[bool]string{false: "mixed", true: "all_adopted"}[all], func(t *testing.T) {
			run, execute := importJournalCLI(t)
			execute("create", "Unrelated fixture seed", "--minor")
			before := relationFixtureBytes(t, "TD_SCHEDULE_STATE")
			second := ""
			if all {
				second = `,"adopt_target_id":"gh-2"`
			}
			input := filepath.Join(t.TempDir(), "adoption.json")
			data := fmt.Sprintf(`[{"issue":{"id":"td-parent","title":"Passive source parent","type":"epic","minor":true},"adopt_target_id":"gh-1"},{"issue":{"id":"td-child","title":"Source child","parent_id":"td-parent","minor":true}%s}]`, second)
			if err := os.WriteFile(input, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			var result ghstore.ImportResult
			out := execute("import", input, "--source", "adoption-cli-fixture")
			if err := json.Unmarshal([]byte(out), &result); err != nil || result.Plan == nil || result.Plan.Rows[0].TargetID != "gh-1" {
				t.Fatal(out, err)
			}
			if all && (result.Created != 0 || result.Applied != 0 || result.Skipped != 2) || !all && (result.Created != 1 || result.Completed != 1 || result.Skipped != 1) {
				t.Fatal("adoption changed task ownership", result)
			}
			// last exposes action_type, rather than the logical journal Action field.
			var rows []struct {
				Action string `json:"action_type"`
				Phase  string
			}
			if out := execute("last", "--n", "1"); json.Unmarshal([]byte(out), &rows) != nil || len(rows) != 1 || rows[0].Action != "import" || rows[0].Phase != "applied" {
				t.Fatal("adoption missing tracked completion", out)
			}
			native, journals := relationFixtureBytes(t, "TD_SCHEDULE_STATE"), relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")
			execute("import", input, "--source", "adoption-cli-fixture")
			if !bytes.Equal(native, relationFixtureBytes(t, "TD_SCHEDULE_STATE")) || !bytes.Equal(journals, relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")) {
				t.Fatal("repeat published another adoption command or task write")
			}
			execute("undo")
			var original, restored struct{ Issues map[string]map[string]any }
			if json.Unmarshal(before, &original) != nil || json.Unmarshal(relationFixtureBytes(t, "TD_SCHEDULE_STATE"), &restored) != nil {
				t.Fatal("invalid native fixture")
			}
			for _, id := range []string{"1", "2"} {
				if !reflect.DeepEqual(original.Issues[id], restored.Issues[id]) {
					t.Fatal("import or undo changed retained task", id)
				}
			}
			if !all {
				out, stderr, err := run("show", result.Plan.Rows[1].TargetID, "--json")
				if err == nil || !strings.Contains(out+stderr, "deleted") {
					t.Fatal("undo failed to compensate new allocation", out, stderr, err)
				}
			}
		})
	}
}

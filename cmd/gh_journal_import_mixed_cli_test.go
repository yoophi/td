package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
)

type importNativeFixtureState struct {
	Issues   map[string]map[string]any
	Comments []map[string]any
}

func readImportNativeFixture(t *testing.T) importNativeFixtureState {
	t.Helper()
	var state importNativeFixtureState
	if err := json.Unmarshal(importFixtureStateBytes(t), &state); err != nil {
		t.Fatal(err)
	}
	return state
}

func recordImportFixturePatches(t *testing.T) func() []map[string]any {
	t.Helper()
	path, err := exec.LookPath("gh")
	if err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	needle := "  if os.environ.get('TD_GH_JOURNAL_FAIL')=='target-lost':"
	if !bytes.Contains(script, []byte(needle)) {
		t.Fatal("missing native PATCH observation seam")
	}
	patchFile := filepath.Join(t.TempDir(), "patches.jsonl")
	t.Setenv("TD_GH_IMPORT_PATCH_LOG", patchFile)
	script = bytes.Replace(script, []byte(needle), []byte("  with pathlib.Path(os.environ['TD_GH_IMPORT_PATCH_LOG']).open('a') as f:f.write(json.dumps({'id':n,'patch':patch})+'\\n')\n"+needle), 1)
	if err := os.WriteFile(path, script, 0755); err != nil {
		t.Fatal(err)
	}
	return func() []map[string]any {
		t.Helper()
		data, err := os.ReadFile(patchFile)
		if err != nil {
			t.Fatal(err)
		}
		var result []map[string]any
		for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
			var row map[string]any
			if err := json.Unmarshal(line, &row); err != nil {
				t.Fatal(err)
			}
			result = append(result, row)
		}
		return result
	}
}

// Reverse both independent graphs while also allocating a new legacy source.
// Neither existing task can acquire its final relationships before detachment.
func TestGitHubJournalCLIImportMixedGraphResumeAndUndo(t *testing.T) {
	run, execute := importJournalCLI(t)
	for _, title := range []string{"First imported task", "Second imported task", "Unrelated task"} {
		execute("create", title, "--minor")
	}
	execute("update", "gh-6", "--parent", "gh-7")
	execute("dep", "add", "gh-7", "gh-6")
	items, err := ghstore.ParseImportJSON([]byte(execute("export", "--all")))
	if err != nil {
		t.Fatal(err)
	}
	selected := []ghstore.ImportItem{}
	for _, item := range items {
		if item.Issue.ID == "gh-6" || item.Issue.ID == "gh-7" {
			selected = append(selected, item)
		}
	}
	if len(selected) != 2 {
		t.Fatal("native export did not retain both selected tasks")
	}
	selected = append(selected, ghstore.ImportItem{SourceNamespace: "mixed-source", Issue: models.Issue{
		ID: "td-new", Title: "New allocation in mixed import", Type: models.TypeTask, Priority: models.PriorityP2, Status: models.StatusOpen, Minor: true,
	}})
	input, err := json.Marshal(selected)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "mixed.json")
	if err := os.WriteFile(file, input, 0600); err != nil {
		t.Fatal(err)
	}
	// Make the live before-state different from the saved source, without
	// inventing or altering native source/export evidence in the fixture.
	execute("update", "gh-6", "--parent", "", "--title", "Original live first task")
	execute("dep", "rm", "gh-7", "gh-6")
	execute("dep", "add", "gh-6", "gh-7")
	execute("update", "gh-7", "--parent", "gh-6", "--title", "Original live second task")
	execute("log", "gh-6", "Retained native destination activity")
	execute("log", "gh-8", "Unrelated native activity")
	original := readImportNativeFixture(t)

	patches := recordImportFixturePatches(t)
	t.Setenv("TD_GH_JOURNAL_FAIL", "target-lost")
	if out, stderr, err := run("import", file, "--force", "--json"); err == nil || !strings.Contains(out+stderr, "connection lost after target") || !strings.Contains(out+stderr, "no automatic retry") {
		t.Fatal("mixed import did not stop on the original detach failure", out, stderr, err)
	}
	t.Setenv("TD_GH_JOURNAL_FAIL", "")
	root := importLastCommand(t, execute)
	if root.Phase != "prepared" || len(root.ImportCommand.Members) != 3 || len(patches()) != 1 {
		t.Fatal("failed import lost its original command or repeated the uncertain PATCH", root.Phase, len(patches()))
	}
	partial := readImportNativeFixture(t)
	if len(partial.Issues) != 9 || partial.Issues["9"] == nil {
		t.Fatal("fixture did not combine an existing overwrite with a real new allocation")
	}
	detachedID := patches()[0]["id"].(string)
	if detachedID != "6" && detachedID != "7" {
		t.Fatal("failure occurred outside the existing-task detachment phase", detachedID)
	}
	var detached ghstore.Record
	if out := execute("show", "gh-"+detachedID); json.Unmarshal([]byte(out), &detached) != nil || detached.Details == nil || detached.ParentID != "" || len(detached.Details.Dependencies) != 0 {
		t.Fatal("failure did not leave an intermediate detachment", detachedID, detached.ParentID)
	}
	// The intermediate PATCH already applies source fields. Original values
	// must remain in the command's Before, not in the detached destination.
	for _, member := range root.ImportCommand.Members {
		if member.Before == nil {
			continue
		}
		id := strings.TrimPrefix(member.TargetID, "gh-")
		if member.Before.Issue.Body != original.Issues[id]["body"] || member.Before.Issue.Title != original.Issues[id]["title"] {
			t.Fatal("detached task replaced the original before-state", member.TargetID)
		}
	}
	state, journals := importFixtureStateBytes(t), relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")
	var observation ghstore.ActionReconcileResult
	if out := execute("undo", "--reconcile", root.ID, "--apply"); json.Unmarshal([]byte(out), &observation) != nil || !observation.Partial || observation.TargetWriteReplayed {
		t.Fatal("partial inspection replayed missing work", out)
	}
	if !bytes.Equal(state, importFixtureStateBytes(t)) || !bytes.Equal(journals, relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")) {
		t.Fatal("partial inspection changed native targets or original command")
	}
	var result ghstore.ImportResult
	if out := execute("import", file, "--force"); json.Unmarshal([]byte(out), &result) != nil || result.Created != 0 || result.Completed != 3 {
		t.Fatal("resume did not finish the original three sources", out)
	}
	completed := importLastCommand(t, execute)
	if completed.ID != root.ID || completed.Phase != "applied" || !reflect.DeepEqual(root.ImportCommand, completed.ImportCommand) || completed.ImportForward == nil || completed.ImportForward.Observation.Proof != "observed" {
		t.Fatal("resume replaced original input, command identity or before-state")
	}
	for _, id := range []string{"gh-6", "gh-7"} {
		var got ghstore.Record
		if out := execute("show", id); json.Unmarshal([]byte(out), &got) != nil || got.Details == nil {
			t.Fatal("missing imported task", out)
		}
		if id == "gh-6" && (got.ParentID != "gh-7" || len(got.Details.Dependencies) != 0) || id == "gh-7" && (got.ParentID != "" || !slices.Equal(got.Details.Dependencies, []string{"gh-6"})) {
			t.Fatal("resume did not restore both source graphs", id, got.ParentID, got.Details.Dependencies)
		}
	}
	after := readImportNativeFixture(t)
	if len(after.Issues) != 9 || after.Issues["9"]["created_at"] != partial.Issues["9"]["created_at"] {
		t.Fatal("resume allocated a duplicate or replaced the native issue identity")
	}
	for _, item := range selected[:2] {
		id := strings.TrimPrefix(item.Issue.ID, "gh-")
		if after.Issues[id]["title"] != item.Issue.Title {
			t.Fatal("resume did not overwrite the selected fields", id)
		}
		for _, field := range []string{"number", "created_at", "user", "url"} {
			if !reflect.DeepEqual(original.Issues[id][field], after.Issues[id][field]) {
				t.Fatal("overwrite changed native identity or authorship", id, field)
			}
		}
	}
	for id, issue := range original.Issues {
		if id != "6" && id != "7" && !reflect.DeepEqual(issue, after.Issues[id]) {
			t.Fatal("mixed import changed an unrelated native task", id)
		}
	}
	for _, comment := range original.Comments {
		if !slices.ContainsFunc(after.Comments, func(got map[string]any) bool { return reflect.DeepEqual(got, comment) }) {
			t.Fatal("mixed import removed or changed original native activity", comment["id"])
		}
	}
	counts := map[string]int{}
	for _, patch := range patches() {
		counts[patch["id"].(string)]++
	}
	if !reflect.DeepEqual(counts, map[string]int{"6": 2, "7": 2, "9": 1}) {
		t.Fatal("resume repeated detach/apply writes", counts)
	}
	state, journals = importFixtureStateBytes(t), relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")
	execute("import", file, "--force")
	if !bytes.Equal(state, importFixtureStateBytes(t)) || !bytes.Equal(journals, relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")) || len(patches()) != 5 {
		t.Fatal("completed mixed import repeated a native or journal write")
	}
	var undone struct {
		ReceiptID   string                     `json:"receipt_id"`
		UndoOutcome *ghstore.ActionUndoOutcome `json:"undo_outcome"`
	}
	if out := execute("undo"); json.Unmarshal([]byte(out), &undone) != nil || undone.ReceiptID != root.ID || undone.UndoOutcome == nil || undone.UndoOutcome.TargetWriteVerified || undone.UndoOutcome.CausalUndoChainEligible {
		t.Fatal("whole undo lost the resumed command's observed attribution", out)
	}
	restored := readImportNativeFixture(t)
	if len(restored.Issues) != len(after.Issues) || !reflect.DeepEqual(after.Comments, restored.Comments) {
		t.Fatal("whole undo replaced native identities or removed import/native history")
	}
	for _, id := range []string{"gh-6", "gh-7"} {
		var got ghstore.Record
		if out := execute("show", id); json.Unmarshal([]byte(out), &got) != nil || got.Details == nil {
			t.Fatal("existing destination was not restored", out)
		}
		if id == "gh-6" && (got.ParentID != "" || !slices.Equal(got.Details.Dependencies, []string{"gh-7"})) || id == "gh-7" && (got.ParentID != "gh-6" || len(got.Details.Dependencies) != 0) {
			t.Fatal("whole undo lost the original graph", id, got.ParentID, got.Details.Dependencies)
		}
		number := strings.TrimPrefix(id, "gh-")
		for _, field := range []string{"title", "labels", "state", "number", "created_at", "user", "url"} {
			if !reflect.DeepEqual(original.Issues[number][field], restored.Issues[number][field]) {
				t.Fatal("undo did not restore original fields/native identity", id, field)
			}
		}
	}
	if out, stderr, err := run("show", "gh-9", "--json"); err == nil || !strings.Contains(out+stderr, "deleted") {
		t.Fatal("undo omitted the allocation made before the interrupted detach", out, stderr, err)
	}
	for _, field := range []string{"number", "created_at", "user", "url", "state"} {
		if !reflect.DeepEqual(after.Issues["9"][field], restored.Issues["9"][field]) {
			t.Fatal("compensation replaced the original allocation identity/history", field)
		}
	}
	for id, issue := range original.Issues {
		if id != "6" && id != "7" && !reflect.DeepEqual(issue, restored.Issues[id]) {
			t.Fatal("whole undo changed an unrelated task", id)
		}
	}
	done := importLastCommand(t, execute)
	if done.Phase != "undone" || !reflect.DeepEqual(done.ImportCommand, root.ImportCommand) || !reflect.DeepEqual(done.ImportForward, completed.ImportForward) {
		t.Fatal("undo changed original or completed forward evidence")
	}
	counts = map[string]int{}
	for _, patch := range patches() {
		counts[patch["id"].(string)]++
	}
	if !reflect.DeepEqual(counts, map[string]int{"6": 4, "7": 4, "9": 2}) {
		t.Fatal("undo did not compensate the complete original and resumed effects exactly once", counts)
	}
	state, journals = importFixtureStateBytes(t), relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")
	execute("undo", "--reconcile", root.ID, "--apply")
	if !bytes.Equal(state, importFixtureStateBytes(t)) || !bytes.Equal(journals, relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")) || len(patches()) != 10 {
		t.Fatal("completed whole undo confirmation repeated writes")
	}
}

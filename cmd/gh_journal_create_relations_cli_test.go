package cmd

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestGitHubJournalCLICreateRelationsWholeCommand(t *testing.T) {
	run, execute := workflowJournalCLI(t)
	execute("create", "Original dependency", "--minor")
	execute("create", "Original reverse target", "--minor")
	before := relationFixtureBytes(t, "TD_SCHEDULE_STATE")
	execute("create", "New related task", "--depends-on", "gh-6", "--blocks", "gh-7", "--minor")
	var source, reverse struct {
		ID      string
		Details struct{ Dependencies []string }
	}
	if out := execute("show", "gh-8"); json.Unmarshal([]byte(out), &source) != nil || !slices.Equal(source.Details.Dependencies, []string{"gh-6"}) {
		t.Fatal("created dependency missing", out)
	}
	if out := execute("show", "gh-7"); json.Unmarshal([]byte(out), &reverse) != nil || !slices.Equal(reverse.Details.Dependencies, []string{"gh-8"}) {
		t.Fatal("created reverse edge missing", out)
	}
	var history []struct {
		ID              string
		Action          string `json:"action_type"`
		Phase           string
		IssueID         string          `json:"entity_id"`
		CreationCommand json.RawMessage `json:"creation_command"`
	}
	if out := execute("last", "--n", "1"); json.Unmarshal([]byte(out), &history) != nil || len(history) != 1 || history[0].Action != "create" || history[0].Phase != "applied" || history[0].IssueID != "gh-8" || len(history[0].CreationCommand) == 0 {
		t.Fatal("creation and both relationship directions are not one recoverable command", out)
	}
	execute("undo")
	var original, restored struct {
		Issues   map[string]map[string]any
		Comments []any
	}
	if json.Unmarshal(before, &original) != nil || json.Unmarshal(relationFixtureBytes(t, "TD_SCHEDULE_STATE"), &restored) != nil {
		t.Fatal("invalid native fixture")
	}
	for _, id := range []string{"6", "7"} {
		for _, field := range []string{"title", "state", "labels"} {
			if !reflect.DeepEqual(original.Issues[id][field], restored.Issues[id][field]) {
				t.Fatal("creation inverse lost an existing task field", id, field)
			}
		}
		if !sameFixtureTaskContentsWithoutAudit(t, original.Issues[id]["body"].(string), restored.Issues[id]["body"].(string)) {
			t.Fatal("creation inverse lost an existing relationship", id)
		}
	}
	if out, stderr, err := run("show", "gh-8", "--json"); err == nil || !strings.Contains(out+stderr, "deleted") {
		t.Fatal("creation inverse did not explicitly hide its allocated task", out, stderr, err)
	}
	if restored.Issues["8"]["title"] != "New related task" || !reflect.DeepEqual(original.Comments, restored.Comments) {
		t.Fatal("creation inverse erased native identity or unrelated activity")
	}
	state, journals := relationFixtureBytes(t, "TD_SCHEDULE_STATE"), relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")
	execute("undo", "--reconcile", history[0].ID, "--apply")
	if !bytes.Equal(state, relationFixtureBytes(t, "TD_SCHEDULE_STATE")) || !bytes.Equal(journals, relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")) {
		t.Fatal("completed creation compensation repeated a write")
	}
}

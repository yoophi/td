package cmd

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestGitHubJournalCLISequentialPairedCommandsUndo(t *testing.T) {
	_, _, run := journalCLI(t)
	useHandoffReviewFixture(t)
	execute := func(args ...string) {
		t.Helper()
		out, stderr, err := run(append(args, "--json")...)
		if err != nil {
			t.Fatal(args, out, stderr, err)
		}
	}
	execute("create", "First")
	execute("create", "Second")
	read := func() map[string]map[string]any {
		t.Helper()
		data, err := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
		if err != nil {
			t.Fatal(err)
		}
		var remote struct{ Issues map[string]map[string]any }
		if err := json.Unmarshal(data, &remote); err != nil {
			t.Fatal(err)
		}
		return remote.Issues
	}
	original := read()
	execute("update", "gh-6", "gh-7", "--title", "First command", "--comment", "First comment")
	execute("update", "gh-6", "gh-7", "--title", "Second command", "--comment", "Second comment")
	execute("undo")
	execute("undo")
	restored := read()
	for _, id := range []string{"6", "7"} {
		for _, field := range []string{"title", "body", "state", "labels"} {
			if !reflect.DeepEqual(original[id][field], restored[id][field]) {
				t.Fatalf("sequential inverses lost original %s %s", id, field)
			}
		}
	}
}

func TestGitHubJournalCLISequentialPairedCommandsRejectsUnprovenBridge(t *testing.T) {
	testSequentialUndoRejectsUnprovenBridge(t, true)
}

func TestGitHubJournalCLISequentialReviewUndo(t *testing.T) {
	_, _, run := journalCLI(t)
	useNativeCommentOrderFixture(t)
	useHandoffReviewFixture(t)
	execute := func(args ...string) {
		t.Helper()
		out, stderr, err := run(append(args, "--json")...)
		if err != nil {
			t.Fatal(args, out, stderr, err)
		}
	}
	execute("create", "First")
	execute("ws", "start", "Sequential review fixture")
	execute("ws", "tag", "gh-6", "--no-start")
	execute("start", "gh-6")
	type nativeState struct {
		Issues   map[string]map[string]any
		Comments []any
	}
	read := func() nativeState {
		t.Helper()
		var state nativeState
		data, err := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &state); err != nil {
			t.Fatal(err)
		}
		return state
	}
	original := read()
	execute("ws", "handoff", "--review", "--done", "Finished")
	execute("update", "gh-6", "--title", "Later command", "--comment", "Later companion")
	execute("undo")
	execute("undo")
	restored := read()
	for _, field := range []string{"title", "body", "state", "labels"} {
		if !reflect.DeepEqual(original.Issues["6"][field], restored.Issues["6"][field]) {
			t.Fatalf("sequential review inverse lost original %s", field)
		}
	}
	if !reflect.DeepEqual(original.Comments, restored.Comments) {
		t.Fatal("sequential inverse retained companion comments or changed unrelated activity")
	}
}

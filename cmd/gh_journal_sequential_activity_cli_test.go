package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestGitHubJournalCLISequentialUpdateActivityUndo(t *testing.T) {
	for _, command := range []string{"due", "defer", "link", "unlink"} {
		t.Run(command, func(t *testing.T) { testSequentialUpdateActivityUndo(t, command, true) })
	}
}

func TestGitHubJournalCLISequentialLaterUpdateActivityUndo(t *testing.T) {
	for _, command := range []string{"due", "defer", "link", "unlink"} {
		t.Run(command, func(t *testing.T) { testSequentialUpdateActivityUndo(t, command, false) })
	}
}

func testSequentialUpdateActivityUndo(t *testing.T, command string, activityFirst bool) {
	t.Helper()
	dir, _, run := journalCLI(t)
	useNativeCommentOrderFixture(t)
	useHandoffReviewFixture(t)
	execute := func(args ...string) {
		t.Helper()
		out, stderr, err := run(append(args, "--json")...)
		if err != nil {
			t.Fatal(args, out, stderr, err)
		}
	}
	execute("create", "Sequential activity fixture")
	if err := os.WriteFile(filepath.Join(dir, "fixture.go"), []byte("package fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if command == "unlink" {
		execute("link", "gh-6", "fixture.go")
	}
	type nativeState struct {
		Issues   map[string]map[string]any
		Comments []any
	}
	read := func() nativeState {
		t.Helper()
		data, err := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
		if err != nil {
			t.Fatal(err)
		}
		var state nativeState
		if err := json.Unmarshal(data, &state); err != nil {
			t.Fatal(err)
		}
		return state
	}
	original := read()
	value := "2099-01-01"
	if command == "link" || command == "unlink" {
		value = "fixture.go"
	}
	if activityFirst {
		execute(command, "gh-6", value)
		execute("update", "gh-6", "--title", "Later command", "--comment", "Later companion")
	} else {
		execute("update", "gh-6", "--title", "Earlier command", "--comment", "Earlier companion")
		execute(command, "gh-6", value)
	}
	execute("undo")
	execute("undo")
	restored := read()
	for _, field := range []string{"title", "body", "state", "labels"} {
		if !reflect.DeepEqual(original.Issues["6"][field], restored.Issues["6"][field]) {
			t.Fatalf("sequential %s inverse lost original %s", command, field)
		}
	}
	if !reflect.DeepEqual(original.Comments, restored.Comments) {
		t.Fatal("sequential inverse retained companion comments or changed unrelated activity")
	}
}

func TestGitHubJournalCLISequentialUpdateActivityRejectsUnprovenBridge(t *testing.T) {
	testSequentialUndoRejectsUnprovenBridge(t, false, []string{"due", "gh-6", "2099-01-01"})
}

func TestGitHubJournalCLISequentialUpdateActivityInverseRecovery(t *testing.T) {
	testSequentialPairedInverseRecovery(t, []string{"due", "gh-6", "2099-01-01"})
}

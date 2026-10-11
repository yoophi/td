package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestGitHubJournalCLISequentialPairedInverseRecovery(t *testing.T) {
	for _, failure := range []string{"undo-completion-denied", "delete-lost"} {
		t.Run(failure, func(t *testing.T) {
			_, _, run := journalCLI(t)
			useHandoffReviewFixture(t)
			execute := func(args ...string) string {
				t.Helper()
				out, stderr, err := run(append(args, "--json")...)
				if err != nil {
					t.Fatal(args, out, stderr, err)
				}
				return out
			}
			read := func() []byte {
				t.Helper()
				data, err := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
				if err != nil {
					t.Fatal(err)
				}
				return data
			}
			execute("create", "First")
			execute("create", "Second")
			original := read()
			execute("update", "gh-6", "gh-7", "--title", "First command", "--comment", "First companion")
			var rows []struct{ ID string }
			last := execute("last", "--n", "1")
			if json.Unmarshal([]byte(last), &rows) != nil || len(rows) != 1 {
				t.Fatal("missing first command", last)
			}
			execute("update", "gh-6", "gh-7", "--title", "Later command", "--comment", "Later companion")
			execute("undo")
			t.Setenv("TD_GH_JOURNAL_FAIL", failure)
			out, stderr, err := run("undo", "--json")
			if err == nil || !strings.Contains(out+stderr, "denied") && !strings.Contains(out+stderr, "lost") {
				t.Fatal("sequential inverse uncertainty hidden", out, stderr, err)
			}
			t.Setenv("TD_GH_JOURNAL_FAIL", "")
			partial := read()
			execute("undo", "--reconcile", rows[0].ID, "--apply")
			if !bytes.Equal(partial, read()) {
				t.Fatal("sequential inverse recovery replayed target writes")
			}
			execute("undo")
			var before, after struct {
				Issues   map[string]map[string]any
				Comments []any
			}
			if json.Unmarshal(original, &before) != nil || json.Unmarshal(read(), &after) != nil {
				t.Fatal("invalid native fixture")
			}
			for _, id := range []string{"6", "7"} {
				for _, field := range []string{"title", "body", "state", "labels"} {
					if !reflect.DeepEqual(before.Issues[id][field], after.Issues[id][field]) {
						t.Fatal("resumed sequential inverse lost original", id, field)
					}
				}
			}
			if !reflect.DeepEqual(before.Comments, after.Comments) {
				t.Fatal("resumed sequential inverse changed unrelated activity or retained companions")
			}
		})
	}
}

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

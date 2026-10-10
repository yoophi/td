package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/marcus/td/internal/ghstore"
)

func TestGitHubJournalCLIUpdateActivityWholeCommand(t *testing.T) {
	for _, command := range []string{"due", "defer", "link", "unlink"} {
		for _, failure := range []string{"", "target-lost", "receipt-denied", "comment-denied", "inverse-receipt-denied"} {
			t.Run(command+"/"+failure, func(t *testing.T) {
				dir, _, run := journalCLI(t)
				useNativeCommentOrderFixture(t)
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
				execute("create", "Compound field and activity")
				if err := os.WriteFile(filepath.Join(dir, "fixture.go"), []byte("package fixture\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if command == "unlink" {
					execute("link", "gh-6", "fixture.go")
				}
				before := read()
				args := []string{command, "gh-6", "2099-01-01"}
				if command == "link" || command == "unlink" {
					args[2] = "fixture.go"
				}
				if failure == "target-lost" || failure == "receipt-denied" {
					t.Setenv("TD_GH_JOURNAL_FAIL", failure)
				}
				if failure == "comment-denied" {
					t.Setenv("TD_SCHEDULE_FAIL_LOG", "1")
				}
				out, stderr, err := run(append(args, "--json")...)
				forwardFailed := failure != "" && failure != "inverse-receipt-denied"
				if (err != nil) != forwardFailed {
					t.Fatal(out, stderr, err)
				}
				t.Setenv("TD_GH_JOURNAL_FAIL", "")
				t.Setenv("TD_SCHEDULE_FAIL_LOG", "")
				var rows []struct {
					ID    string
					Group *ghstore.ActionGroup
				}
				last := execute("last", "--n", "1")
				if json.Unmarshal([]byte(last), &rows) != nil || len(rows) != 1 || rows[0].Group == nil || rows[0].Group.UpdateActivity == nil || rows[0].Group.Command != command {
					t.Fatal("command lost its complete input aggregate", last)
				}
				if forwardFailed {
					actual := read()
					execute("undo", "--reconcile", rows[0].ID, "--apply")
					if !bytes.Equal(actual, read()) {
						t.Fatal("recovery replayed a target write")
					}
				}
				if failure == "inverse-receipt-denied" {
					t.Setenv("TD_GH_JOURNAL_FAIL", "undo-completion-denied")
					out, stderr, err = run("undo", "--json")
					if err == nil || !strings.Contains(out+stderr, "denied") {
						t.Fatal("lost inverse receipt was hidden", out, stderr, err)
					}
					t.Setenv("TD_GH_JOURNAL_FAIL", "")
					actual := read()
					execute("undo", "--reconcile", rows[0].ID, "--apply")
					if !bytes.Equal(actual, read()) {
						t.Fatal("inverse recovery replayed a target write")
					}
				}
				execute("undo")
				var original, restored struct {
					Issues   map[string]map[string]any
					Comments []any
				}
				if json.Unmarshal(before, &original) != nil || json.Unmarshal(read(), &restored) != nil {
					t.Fatal("bad fixture")
				}
				for _, field := range []string{"title", "body", "labels", "state"} {
					if !reflect.DeepEqual(original.Issues["6"][field], restored.Issues["6"][field]) {
						t.Fatal("whole-command inverse lost original contents", field)
					}
				}
				if !reflect.DeepEqual(original.Comments, restored.Comments) {
					t.Fatal("inverse did not remove only its companion activity")
				}
				actual := read()
				execute("undo", "--reconcile", rows[0].ID, "--apply")
				if !bytes.Equal(actual, read()) {
					t.Fatal("completed confirmation was not read-only")
				}
			})
		}
	}
}

func TestGitHubJournalCLIUpdateActivityRejectsForeignChanges(t *testing.T) {
	for _, mode := range []string{"source", "activity", "receipt-identity"} {
		t.Run(mode, func(t *testing.T) {
			_, _, run := journalCLI(t)
			useNativeCommentOrderFixture(t)
			for _, args := range [][]string{{"create", "Foreign-change fixture"}, {"due", "gh-6", "2099-01-01"}} {
				if out, stderr, err := run(args...); err != nil {
					t.Fatal(out, stderr, err)
				}
			}
			stateFile, journalFile := os.Getenv("TD_SCHEDULE_STATE"), os.Getenv("TD_GH_FIXTURE_JOURNAL")
			file := stateFile
			if mode == "receipt-identity" {
				file = journalFile
			}
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			var native struct {
				Issues   map[string]map[string]any `json:"issues,omitempty"`
				Comments []map[string]any          `json:"comments"`
			}
			if json.Unmarshal(data, &native) != nil {
				t.Fatal("invalid fixture")
			}
			switch mode {
			case "source":
				native.Issues["6"]["title"] = "Concurrent same-clock change"
			case "activity":
				native.Comments[0]["body"] = "Externally edited activity, unchanged native clock"
			case "receipt-identity":
				body := native.Comments[2]["body"].(string)
				parts := strings.SplitN(body, "<!-- td:action:v22\n", 2)
				if len(parts) != 2 {
					t.Fatal("field member fixture not found")
				}
				var action ghstore.ActionJournal
				if json.Unmarshal([]byte(strings.TrimSuffix(parts[1], "\n-->")), &action) != nil {
					t.Fatal("invalid member")
				}
				native.Comments[2]["body"] = strings.ReplaceAll(body, action.OperationID, "td-op-foreign-identity")
			}
			data, err = json.Marshal(native)
			if err != nil || os.WriteFile(file, data, 0600) != nil {
				t.Fatal("failed to inject external change", err)
			}
			beforeState, _ := os.ReadFile(stateFile)
			beforeJournal, _ := os.ReadFile(journalFile)
			if out, stderr, err := run("undo", "--json"); err == nil {
				t.Fatal("foreign change was overwritten", out, stderr)
			}
			afterState, _ := os.ReadFile(stateFile)
			afterJournal, _ := os.ReadFile(journalFile)
			if !bytes.Equal(beforeState, afterState) || !bytes.Equal(beforeJournal, afterJournal) {
				t.Fatal("conflict detection wrote before rejecting foreign data")
			}
		})
	}
}

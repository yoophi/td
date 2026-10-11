package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/marcus/td/internal/ghstore"
)

func TestGitHubJournalCLIUpdateCommentsWholeCommand(t *testing.T) {
	for _, failure := range []string{"", "single", "closed", "comment-only", "field-denied", "comment-denied", "receipt-denied", "target-lost", "second-comment-lost", "inverse-receipt-denied", "inverse-delete-lost"} {
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
			execute("create", "First", "--description", "First description")
			execute("create", "Second", "--description", "Second description")
			ids := []string{"gh-6", "gh-7"}
			if failure == "single" {
				ids = ids[:1]
			}
			if failure == "closed" {
				ids[1] = "gh-3"
			}
			execute("log", "gh-6", "Unrelated activity")
			before := read()
			switch failure {
			case "comment-only":
				// A comment-only batch must not issue a synthetic field PATCH.
				t.Setenv("TD_SCHEDULE_FAIL_WRITE", "1")
			case "field-denied":
				t.Setenv("TD_SCHEDULE_FAIL_WRITE", "1")
			case "comment-denied":
				t.Setenv("TD_SCHEDULE_FAIL_LOG", "1")
			case "receipt-denied", "target-lost", "second-comment-lost":
				t.Setenv("TD_GH_JOURNAL_FAIL", failure)
			}
			args := append([]string{"update"}, ids...)
			if failure != "comment-only" {
				args = append(args, "--priority", "P1", "--append", "--description", "Appended")
			}
			args = append(args, "--comment", "Command companion comment", "--json")
			out, stderr, err := run(args...)
			forwardFailed := failure == "field-denied" || failure == "comment-denied" || failure == "receipt-denied" || failure == "target-lost" || failure == "second-comment-lost"
			if (err != nil) != forwardFailed {
				t.Fatal(out, stderr, err)
			}
			if forwardFailed && !strings.Contains(out+stderr, "group ghc-") {
				t.Fatal("partial error omitted its original group", out, stderr, err)
			}
			t.Setenv("TD_SCHEDULE_FAIL_WRITE", "")
			t.Setenv("TD_SCHEDULE_FAIL_LOG", "")
			t.Setenv("TD_GH_JOURNAL_FAIL", "")
			var rows []struct {
				ID    string
				Group *ghstore.ActionGroup
			}
			last := execute("last", "--n", "1")
			if json.Unmarshal([]byte(last), &rows) != nil || len(rows) != 1 || rows[0].Group == nil || rows[0].Group.Command != "update-comments" || len(rows[0].Group.Members) != len(ids) {
				t.Fatal("field/comment batch was not recorded as one command", last)
			}
			if forwardFailed {
				actual := read()
				execute("undo", "--reconcile", rows[0].ID, "--apply")
				if !bytes.Equal(actual, read()) {
					t.Fatal("forward recovery repeated target writes")
				}
			}
			if failure == "inverse-receipt-denied" || failure == "inverse-delete-lost" {
				mode := "undo-completion-denied"
				if failure == "inverse-delete-lost" {
					mode = "delete-lost"
				}
				t.Setenv("TD_GH_JOURNAL_FAIL", mode)
				out, stderr, err := run("undo", "--json")
				if err == nil || !strings.Contains(out+stderr, "denied") && !strings.Contains(out+stderr, "lost") {
					t.Fatal("inverse uncertainty hidden", out, stderr, err)
				}
				t.Setenv("TD_GH_JOURNAL_FAIL", "")
				actual := read()
				execute("undo", "--reconcile", rows[0].ID, "--apply")
				if !bytes.Equal(actual, read()) {
					t.Fatal("inverse recovery repeated target writes")
				}
			}
			execute("undo")
			var original, restored struct {
				Issues   map[string]map[string]any
				Comments []any
			}
			if json.Unmarshal(before, &original) != nil || json.Unmarshal(read(), &restored) != nil {
				t.Fatal("invalid fixture")
			}
			for _, selected := range ids {
				id := strings.TrimPrefix(selected, "gh-")
				for _, field := range []string{"title", "body", "labels", "state"} {
					if !reflect.DeepEqual(original.Issues[id][field], restored.Issues[id][field]) {
						t.Fatal("whole-command inverse lost original", id, field)
					}
				}
			}
			if !reflect.DeepEqual(original.Comments, restored.Comments) {
				t.Fatal("whole-command inverse retained companion comments or changed unrelated activity")
			}
			actual := read()
			execute("undo", "--reconcile", rows[0].ID, "--apply")
			if !bytes.Equal(actual, read()) {
				t.Fatal("completed confirmation repeated target writes")
			}
			if failure == "receipt-denied" || failure == "target-lost" || failure == "second-comment-lost" || failure == "inverse-receipt-denied" || failure == "inverse-delete-lost" {
				for _, a := range fixtureAllJournals(t) {
					if a.Group != nil && a.Group.UpdateComments != nil && a.Group.Inverse.Proof != "group-contents-observed" {
						t.Fatal("weaker outcome promoted to verified", a.Group.Inverse)
					}
				}
			}
		})
	}
}

func TestGitHubJournalCLIUpdateCommentsRejectsForeignChanges(t *testing.T) {
	for _, mode := range []string{"source", "comment", "receipt-identity"} {
		t.Run(mode, func(t *testing.T) {
			_, _, run := journalCLI(t)
			useHandoffReviewFixture(t)
			for _, args := range [][]string{{"create", "First"}, {"create", "Second"}, {"update", "gh-6", "gh-7", "--title", "Changed", "--comment", "Companion"}} {
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
			var native map[string]any
			if err := json.Unmarshal(data, &native); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "source":
				native["issues"].(map[string]any)["6"].(map[string]any)["title"] = "Foreign same-clock edit"
			case "comment":
				native["comments"].([]any)[0].(map[string]any)["body"] = "Foreign same-clock comment edit"
			case "receipt-identity":
				found := false
				for _, row := range native["comments"].([]any) {
					comment := row.(map[string]any)
					body := comment["body"].(string)
					parts := strings.SplitN(body, "<!-- td:action:v26\n", 2)
					if len(parts) != 2 {
						continue
					}
					var action ghstore.ActionJournal
					if err := json.Unmarshal([]byte(strings.TrimSuffix(parts[1], "\n-->")), &action); err != nil {
						t.Fatal(err)
					}
					if action.Action == "update" && action.IssueID == "gh-7" {
						comment["body"] = strings.ReplaceAll(body, action.OperationID, "td-op-foreign-receipt-identity")
						found = true
						break
					}
				}
				if !found {
					t.Fatal("no original member fixture")
				}
			}
			data, err = json.Marshal(native)
			if err != nil || os.WriteFile(file, data, 0600) != nil {
				t.Fatal("could not edit native fixture", err)
			}
			stateBefore, err := os.ReadFile(stateFile)
			if err != nil {
				t.Fatal(err)
			}
			journalBefore, err := os.ReadFile(journalFile)
			if err != nil {
				t.Fatal(err)
			}
			out, stderr, err := run("undo", "--json")
			if err == nil || !strings.Contains(out+stderr, "changed") && !strings.Contains(out+stderr, "conflict") {
				t.Fatal("foreign change accepted", out, stderr, err)
			}
			stateAfter, readErr := os.ReadFile(stateFile)
			if readErr != nil || !bytes.Equal(stateBefore, stateAfter) {
				t.Fatal("foreign target was overwritten", readErr)
			}
			journalAfter, readErr := os.ReadFile(journalFile)
			if readErr != nil || !bytes.Equal(journalBefore, journalAfter) {
				t.Fatal("foreign evidence caused an inverse receipt write", readErr)
			}
		})
	}
}

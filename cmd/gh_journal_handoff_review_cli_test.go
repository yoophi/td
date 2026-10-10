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

func TestGitHubJournalCLIHandoffReviewWholeCommand(t *testing.T) {
	for _, failure := range []string{"", "single", "all-review", "handoff-only", "comment-denied", "review-denied", "receipt-denied", "target-lost", "inverse-receipt-denied", "second-comment-lost", "inverse-delete-lost"} {
		t.Run(failure, func(t *testing.T) {
			_, _, run := journalCLI(t)
			useNativeCommentOrderFixture(t)
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
			execute("create", "Review group first")
			ids := []string{"gh-6"}
			if failure != "single" {
				execute("create", "Review group second")
				ids = append(ids, "gh-7")
			}
			execute("ws", "start", "Persistent review group")
			execute(append(append([]string{"ws", "tag"}, ids...), "--no-start")...)
			if failure != "handoff-only" {
				execute("start", "gh-6")
			}
			if failure == "all-review" {
				execute("start", "gh-7")
			}
			// Preserve unrelated activity through the entire inverse.
			execute("log", "gh-6", "Earlier unrelated activity")
			before := read()
			switch failure {
			case "comment-denied":
				t.Setenv("TD_SCHEDULE_FAIL_LOG", "1")
			case "review-denied":
				t.Setenv("TD_SCHEDULE_FAIL_WRITE", "1")
			case "receipt-denied", "target-lost", "second-comment-lost":
				t.Setenv("TD_GH_JOURNAL_FAIL", failure)
			}
			out, stderr, err := run("ws", "handoff", "--review", "--done", "Finished", "--json")
			forwardFailed := failure == "comment-denied" || failure == "review-denied" || failure == "receipt-denied" || failure == "target-lost" || failure == "second-comment-lost"
			if (err != nil) != forwardFailed {
				t.Fatal(out, stderr, err)
			}
			t.Setenv("TD_SCHEDULE_FAIL_LOG", "")
			t.Setenv("TD_SCHEDULE_FAIL_WRITE", "")
			t.Setenv("TD_GH_JOURNAL_FAIL", "")
			var rows []struct {
				ID    string
				Group *ghstore.ActionGroup
			}
			last := execute("last", "--n", "1")
			if json.Unmarshal([]byte(last), &rows) != nil || len(rows) != 1 || rows[0].Group == nil || rows[0].Group.HandoffReview == nil || rows[0].Group.Command != "ws-handoff-review" {
				t.Fatal("missing whole command", last)
			}
			var memberID string
			for _, r := range fixtureAllJournals(t) {
				if r.GroupContract == "handoff-review" {
					memberID = r.OperationID
					break
				}
			}
			if memberID == "" {
				t.Fatal("group did not record an attempted member")
			}
			if forwardFailed {
				actual := read()
				execute("undo", "--reconcile", rows[0].ID, "--apply")
				if !bytes.Equal(actual, read()) {
					t.Fatal("recovery replayed target writes")
				}
			}
			if failure == "inverse-receipt-denied" || failure == "inverse-delete-lost" {
				t.Setenv("TD_GH_JOURNAL_FAIL", "undo-completion-denied")
				if failure == "inverse-delete-lost" {
					t.Setenv("TD_GH_JOURNAL_FAIL", "delete-lost")
				}
				out, stderr, err = run("undo", "--json")
				if err == nil || !strings.Contains(out+stderr, "denied") && !strings.Contains(out+stderr, "lost") {
					t.Fatal("inverse uncertainty hidden", out, stderr, err)
				}
				t.Setenv("TD_GH_JOURNAL_FAIL", "")
				actual := read()
				execute("undo", "--reconcile", rows[0].ID, "--apply")
				if !bytes.Equal(actual, read()) {
					t.Fatal("inverse recovery replayed target writes")
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
			for _, id := range ids {
				for _, field := range []string{"title", "body", "labels", "state"} {
					if !reflect.DeepEqual(original.Issues[strings.TrimPrefix(id, "gh-")][field], restored.Issues[strings.TrimPrefix(id, "gh-")][field]) {
						t.Fatal("whole inverse lost original", id, field)
					}
				}
			}
			if !reflect.DeepEqual(original.Comments, restored.Comments) {
				t.Fatal("undo changed unrelated activity or retained handoffs")
			}
			actual := read()
			execute("undo", "--reconcile", rows[0].ID, "--apply")
			if !bytes.Equal(actual, read()) {
				t.Fatal("completed confirmation wrote target")
			}
			// Recovery is never upgraded to a verified target write.
			if failure == "receipt-denied" || failure == "target-lost" || failure == "inverse-receipt-denied" || failure == "second-comment-lost" || failure == "inverse-delete-lost" {
				for _, a := range fixtureAllJournals(t) {
					if a.Group != nil && a.Group.HandoffReview != nil && a.Group.Inverse.Proof != "group-contents-observed" {
						t.Fatal("observed outcome promoted to verified", a.Group.Inverse)
					}
				}
			}
		})
	}
}

func TestGitHubJournalCLIHandoffReviewRejectsForeignChanges(t *testing.T) {
	for _, mode := range []string{"unreviewed-source", "handoff", "review-receipt", "native-events"} {
		t.Run(mode, func(t *testing.T) {
			_, _, run := journalCLI(t)
			useNativeCommentOrderFixture(t)
			useHandoffReviewFixture(t)
			if mode == "native-events" {
				t.Setenv("TD_GH_FIXTURE_NATIVE_EVENTS", "1")
			}
			for _, args := range [][]string{{"create", "Review first"}, {"create", "Handoff second"}, {"ws", "start", "Conflict fixture"}, {"ws", "tag", "gh-6", "gh-7", "--no-start"}, {"start", "gh-6"}, {"ws", "handoff", "--review", "--done", "Finished"}} {
				if out, stderr, err := run(args...); err != nil {
					t.Fatal(args, out, stderr, err)
				}
			}
			stateFile, journalFile := os.Getenv("TD_SCHEDULE_STATE"), os.Getenv("TD_GH_FIXTURE_JOURNAL")
			file := stateFile
			if mode == "review-receipt" || mode == "native-events" {
				file = journalFile
			}
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			var native map[string]any
			if json.Unmarshal(data, &native) != nil {
				t.Fatal("invalid fixture")
			}
			switch mode {
			case "unreviewed-source":
				native["issues"].(map[string]any)["7"].(map[string]any)["title"] = "Concurrent change on handoff-only member"
			case "handoff":
				native["comments"].([]any)[1].(map[string]any)["body"] = "Edited second native handoff at original timestamp"
			case "review-receipt":
				changed := false
				for _, row := range native["comments"].([]any) {
					r := row.(map[string]any)
					body := r["body"].(string)
					parts := strings.SplitN(body, "<!-- td:action:v24\n", 2)
					if len(parts) != 2 {
						continue
					}
					var a ghstore.ActionJournal
					if json.Unmarshal([]byte(strings.TrimSuffix(parts[1], "\n-->")), &a) != nil {
						t.Fatal("invalid effect")
					}
					if a.Action == "update" {
						r["body"] = strings.ReplaceAll(body, a.OperationID, "td-op-foreign-receipt")
						changed = true
						break
					}
				}
				if !changed {
					t.Fatal("review effect not found")
				}
			case "native-events":
				native["native_events"].(map[string]any)["6"] = []any{map[string]any{"id": 1, "event": "closed"}, map[string]any{"id": 2, "event": "reopened"}}
			}
			data, err = json.Marshal(native)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, data, 0600); err != nil {
				t.Fatal(err)
			}
			beforeState, _ := os.ReadFile(stateFile)
			beforeJournal, _ := os.ReadFile(journalFile)
			out, stderr, err := run("undo", "--json")
			if err == nil || !strings.Contains(out+stderr, "changed") && !strings.Contains(out+stderr, "unverified") {
				t.Fatal("foreign evidence was not explicitly rejected", out, stderr, err)
			}
			afterState, _ := os.ReadFile(stateFile)
			afterJournal, _ := os.ReadFile(journalFile)
			if !bytes.Equal(beforeState, afterState) || !bytes.Equal(beforeJournal, afterJournal) {
				t.Fatal("conflict detection wrote before rejecting external data")
			}
		})
	}
}

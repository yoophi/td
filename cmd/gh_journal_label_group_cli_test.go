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

func TestGitHubJournalCLILabelRepairGroup(t *testing.T) {
	for _, kind := range []string{"state", "priority"} {
		for _, failure := range []string{"", "receipt-denied", "second-target-lost", "foreign-inverse"} {
			t.Run(kind+"/"+failure, func(t *testing.T) {
				dir, _, run := journalCLI(t)
				execute := func(args ...string) string {
					t.Helper()
					out, stderr, err := run(append(args, "--json")...)
					if err != nil {
						t.Fatal(args, out, stderr, err)
					}
					return out
				}
				read := func(file string) []byte {
					t.Helper()
					data, err := os.ReadFile(file)
					if err != nil {
						t.Fatal(err)
					}
					return data
				}
				execute("create", "Unselected task")
				stateFile, journalFile := os.Getenv("TD_SCHEDULE_STATE"), os.Getenv("TD_GH_FIXTURE_JOURNAL")
				var original struct {
					Issues   map[string]map[string]any `json:"issues"`
					Comments []any                     `json:"comments"`
				}
				if err := json.Unmarshal(read(stateFile), &original); err != nil {
					t.Fatal(err)
				}
				// Synthetic native edits: only open #1, closed #3 and deleted #4
				// need repair. Retain user labels and different per-task status.
				for id, issue := range original.Issues {
					priority := "P2"
					if id == "2" {
						priority = "P0"
					}
					issue["labels"] = []map[string]string{{"name": "td:" + issue["state"].(string)}, {"name": "td:priority:" + priority}}
				}
				for _, id := range []string{"1", "3", "4"} {
					original.Issues[id]["labels"] = []map[string]string{{"name": "bug"}, {"name": "td:priority:custom"}}
				}
				before, err := json.Marshal(original)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(stateFile, before, 0600); err != nil {
					t.Fatal(err)
				}
				args := []string{"config", "sync-" + kind + "-labels"}
				if kind == "priority" {
					journalBefore := read(journalFile)
					execute(args...)
					if !bytes.Equal(before, read(stateFile)) || !bytes.Equal(journalBefore, read(journalFile)) {
						t.Fatal("preview wrote native data or journal")
					}
					args = append(args, "--apply")
				}
				partial := failure == "receipt-denied" || failure == "second-target-lost"
				if failure == "second-target-lost" {
					file := filepath.Join(filepath.Dir(stateFile), "gh")
					script := string(read(file))
					needle := "if os.environ.get('TD_GH_JOURNAL_FAIL')=='target-lost':"
					if strings.Count(script, needle) != 1 {
						t.Fatal("fixture target result boundary changed")
					}
					replacement := "if os.environ.get('TD_GH_JOURNAL_FAIL')=='target-lost' or (os.environ.get('TD_GH_JOURNAL_FAIL')=='second-target-lost' and n=='3'):"
					if err := os.WriteFile(file, []byte(strings.Replace(script, needle, replacement, 1)), 0755); err != nil {
						t.Fatal(err)
					}
				}
				if partial {
					t.Setenv("TD_GH_JOURNAL_FAIL", failure)
				}
				out, stderr, err := run(append(args, "--json")...)
				if (err != nil) != partial {
					t.Fatal(out, stderr, err)
				}
				cause := "HTTP 403 journal receipt denied"
				if failure == "second-target-lost" {
					cause = "connection lost after target"
					if !strings.Contains(out+stderr, "1 "+kind+" label repairs saved") {
						t.Fatal("lost count of verified earlier repairs", out, stderr)
					}
				}
				if partial && (!strings.Contains(out+stderr, "group ghc-") || !strings.Contains(out+stderr, cause)) {
					t.Fatal("partial repair lost its group identity or original cause", out, stderr)
				}
				t.Setenv("TD_GH_JOURNAL_FAIL", "")
				var rows []struct {
					ID    string
					Group *ghstore.ActionGroup
				}
				last := execute("last", "--n", "1")
				if json.Unmarshal([]byte(last), &rows) != nil || len(rows) != 1 || rows[0].Group == nil || !rows[0].Group.Planned || rows[0].Group.Command != "update" || len(rows[0].Group.Members) != 3 {
					t.Fatal("label repairs escaped the complete planned group", last)
				}
				for _, m := range rows[0].Group.Members {
					if m.Input == nil || m.Input.Target.Body != m.Before.Body || m.Input.Target.Title != m.Before.Title || m.Input.Target.State != m.Before.State || !reflect.DeepEqual(m.Input.Target.Labels, []string{"bug", "td:priority:custom", "td:" + m.Before.State, "td:priority:P2"}) {
						t.Fatal("plan changed metadata/native state or omitted complete labels", m)
					}
				}
				if failure == "foreign-inverse" {
					var changed map[string]any
					if err := json.Unmarshal(read(stateFile), &changed); err != nil {
						t.Fatal(err)
					}
					changed["issues"].(map[string]any)["3"].(map[string]any)["title"] = "Foreign same-clock edit"
					data, err := json.Marshal(changed)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(stateFile, data, 0600); err != nil {
						t.Fatal(err)
					}
					journal := read(journalFile)
					out, stderr, err := run("undo", "--json")
					if err == nil || !strings.Contains(out+stderr, "conflict") || !bytes.Equal(data, read(stateFile)) || !bytes.Equal(journal, read(journalFile)) {
						t.Fatal("foreign member allowed a partial inverse or intent write", out, stderr, err)
					}
					return
				}
				if partial {
					actual := read(stateFile)
					recovery := execute("undo", "--reconcile", rows[0].ID, "--apply")
					if !bytes.Equal(actual, read(stateFile)) || !strings.Contains(recovery, `"partial":true`) {
						t.Fatal("recovery replayed a repair or lost partial result", recovery)
					}
				} else {
					actual, journal := read(stateFile), read(journalFile)
					execute(args...)
					if !bytes.Equal(actual, read(stateFile)) || !bytes.Equal(journal, read(journalFile)) {
						t.Fatal("repeated repair wrote unchanged tasks or an empty group")
					}
				}
				execute("undo")
				var restored struct{ Issues map[string]map[string]any }
				// Decode both to compare native JSON types, including labels.
				if json.Unmarshal(before, &original) != nil || json.Unmarshal(read(stateFile), &restored) != nil {
					t.Fatal("invalid restored fixture")
				}
				for id, issue := range original.Issues {
					for _, field := range []string{"title", "body", "state", "labels"} {
						if !reflect.DeepEqual(issue[field], restored.Issues[id][field]) {
							t.Fatal("inverse lost original labels/task/visibility", id, field)
						}
					}
				}
				actual, journal := read(stateFile), read(journalFile)
				execute("undo", "--reconcile", rows[0].ID, "--apply")
				if !bytes.Equal(actual, read(stateFile)) || !bytes.Equal(journal, read(journalFile)) {
					t.Fatal("inverse confirmation replayed a write")
				}
				if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
					t.Fatal("SQLite fallback", err)
				}
			})
		}
	}
}

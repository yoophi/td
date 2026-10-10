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

func TestGitHubJournalCLIPlannedScalarUpdateGroup(t *testing.T) {
	for _, partial := range []bool{false, true} {
		name := "complete"
		if partial {
			name = "receipt-denied"
		}
		t.Run(name, func(t *testing.T) {
			dir, _, run := journalCLI(t)
			execute := func(args ...string) string {
				t.Helper()
				out, stderr, err := run(append(args, "--json")...)
				if err != nil {
					t.Fatal(args, out, stderr, err)
				}
				return out
			}
			execute("create", "First scalar", "--description", "Original first")
			execute("create", "Second scalar", "--description", "Original second", "--minor")
			execute("close", "gh-7", "--reason", "Closed scalar fixture")
			readState := func() []byte {
				t.Helper()
				data, err := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
				if err != nil {
					t.Fatal(err)
				}
				return data
			}
			before := readState()
			if partial {
				t.Setenv("TD_GH_JOURNAL_FAIL", "receipt-denied")
			}
			out, stderr, err := run("update", "gh-6", "gh-7", "--priority", "P1", "--description", "Append once", "--append", "--sprint", "Batch", "--json")
			if (err != nil) != partial {
				t.Fatal(out, stderr, err)
			}
			t.Setenv("TD_GH_JOURNAL_FAIL", "")
			var rows []struct {
				ID    string
				Group *ghstore.ActionGroup
			}
			last := execute("last", "--n", "1")
			if json.Unmarshal([]byte(last), &rows) != nil || len(rows) != 1 || rows[0].Group == nil || rows[0].Group.Command != "update" || len(rows[0].Group.Members) != 2 {
				t.Fatal("scalar updates escaped command group", last)
			}
			for i, m := range rows[0].Group.Members {
				if m.Input == nil || strings.Count(m.Input.Target.Body, "Append once") != 1 || !strings.Contains(m.Input.Target.Body, []string{"Original first", "Original second"}[i]) {
					t.Fatal("complete input was not frozen for all members", m)
				}
			}
			if partial {
				actual := readState()
				recovery := execute("undo", "--reconcile", rows[0].ID, "--apply")
				if !bytes.Equal(actual, readState()) || !strings.Contains(recovery, `"partial":true`) {
					t.Fatal("recovery replayed target or lost partial outcome", recovery)
				}
			} else {
				for _, id := range []string{"gh-6", "gh-7"} {
					if show := execute("show", id); !strings.Contains(show, "Append once") || !strings.Contains(show, `"priority":"P1"`) {
						t.Fatal("batch lost update fields", show)
					}
				}
			}
			execute("undo")
			var original, restored struct{ Issues map[string]map[string]any }
			if json.Unmarshal(before, &original) != nil || json.Unmarshal(readState(), &restored) != nil {
				t.Fatal("invalid native fixture")
			}
			for _, id := range []string{"6", "7"} {
				for _, field := range []string{"title", "body", "state", "labels"} {
					if !reflect.DeepEqual(original.Issues[id][field], restored.Issues[id][field]) {
						t.Fatal("batch inverse did not restore original task", id, field)
					}
				}
			}
			if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
				t.Fatal("SQLite fallback", err)
			}
		})
	}
}

func TestGitHubJournalCLIPlannedUpdateForeignMemberFence(t *testing.T) {
	for _, stage := range []string{"after-member-intent", "before-inverse"} {
		t.Run(stage, func(t *testing.T) {
			_, _, run := journalCLI(t)
			for _, title := range []string{"First", "Second"} {
				if out, stderr, err := run("create", title, "--json"); err != nil {
					t.Fatal(out, stderr, err)
				}
			}
			stateFile := os.Getenv("TD_SCHEDULE_STATE")
			read := func(file string) []byte {
				t.Helper()
				data, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				return data
			}
			if stage == "after-member-intent" {
				// Edit the other task after the first member intent POST, so the
				// early selection and attach fences have already passed.
				file := filepath.Join(filepath.Dir(stateFile), "gh")
				script := string(read(file))
				needle := " if os.environ.get('TD_GH_JOURNAL_FAIL')=='intent-lost':"
				injection := ` if '"group_operation_id":' in body:
  fixture_path=pathlib.Path(os.environ['TD_SCHEDULE_STATE']);foreign=json.loads(fixture_path.read_text());foreign['issues']['7']['title']='Foreign edit';fixture_path.write_text(json.dumps(foreign))
`
				if strings.Count(script, needle) != 1 {
					t.Fatal("fixture intent boundary changed")
				}
				if err := os.WriteFile(file, []byte(strings.Replace(script, needle, injection+needle, 1)), 0755); err != nil {
					t.Fatal(err)
				}
				if out, stderr, err := run("update", "gh-6", "gh-7", "--title", "Batch edit", "--json"); err == nil || !strings.Contains(out+stderr, "conflict") {
					t.Fatal("foreign sibling was ignored after intent", out, stderr, err)
				}
				if fixtureTargetTitle(t, "6") != "First" || fixtureTargetTitle(t, "7") != "Foreign edit" {
					t.Fatal("known foreign sibling edit allowed a target PATCH")
				}
				for _, action := range fixtureAllJournals(t) {
					if action.GroupOperationID != "" && action.Phase != "prepared" {
						t.Fatal("stopped batch fabricated a member result")
					}
				}
				return
			}
			if out, stderr, err := run("update", "gh-6", "gh-7", "--title", "Batch edit", "--json"); err != nil {
				t.Fatal(out, stderr, err)
			}
			var state map[string]any
			if err := json.Unmarshal(read(stateFile), &state); err != nil {
				t.Fatal(err)
			}
			state["issues"].(map[string]any)["7"].(map[string]any)["title"] = "Foreign same-clock edit"
			data, err := json.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(stateFile, data, 0600); err != nil {
				t.Fatal(err)
			}
			journalFile := os.Getenv("TD_GH_FIXTURE_JOURNAL")
			before := read(journalFile)
			if out, stderr, err := run("undo", "--json"); err == nil || !strings.Contains(out+stderr, "conflict") {
				t.Fatal(out, stderr, err)
			}
			if !bytes.Equal(data, read(stateFile)) || !bytes.Equal(before, read(journalFile)) {
				t.Fatal("foreign member was partially undone or inverse intent was written")
			}
		})
	}
}

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

func TestGitHubJournalCLIPlannedRejectDeleteRestore(t *testing.T) {
	for _, command := range []string{"reject", "delete", "restore"} {
		for _, partial := range []bool{false, true} {
			name := command + "/complete"
			if partial {
				name = command + "/receipt-denied"
			}
			t.Run(name, func(t *testing.T) {
				_, _, run := journalCLI(t)
				execute := func(args ...string) string {
					t.Helper()
					out, stderr, err := run(append(args, "--json")...)
					if err != nil {
						t.Fatal(args, out, stderr, err)
					}
					return out
				}
				for _, title := range []string{"First planned task", "Second planned task", "Already requested state"} {
					execute("create", title, "--minor")
				}
				switch command {
				case "reject":
					execute("review", "gh-6", "gh-7", "--reason", "Minor review fixture")
				case "delete":
					execute("close", "gh-7", "--reason", "Closed deletion fixture")
					execute("delete", "gh-8", "--reason", "Already deleted")
				case "restore":
					execute("delete", "gh-6", "--reason", "Restore fixture")
					execute("delete", "gh-7", "--reason", "Restore fixture")
				}
				read := func() []byte {
					t.Helper()
					data, err := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
					if err != nil {
						t.Fatal(err)
					}
					return data
				}
				before := read()
				if partial {
					t.Setenv("TD_GH_JOURNAL_FAIL", "receipt-denied")
				}
				out, stderr, err := run(command, "gh-6", "gh-7", "gh-8", "--reason", "Frozen command", "--json")
				if (err != nil) != partial {
					t.Fatal(out, stderr, err)
				}
				t.Setenv("TD_GH_JOURNAL_FAIL", "")
				var rows []struct {
					ID    string
					Group *ghstore.ActionGroup
				}
				last := execute("last", "--n", "1")
				if json.Unmarshal([]byte(last), &rows) != nil || len(rows) != 1 || rows[0].Group == nil || !rows[0].Group.Planned || rows[0].Group.Command != command {
					t.Fatal("command escaped declared plan", last)
				}
				members := rows[0].Group.Members
				if len(members) != 3 || members[0].Noop || members[1].Noop || !members[2].Noop {
					t.Fatal("no-op was not fixed in original plan", members)
				}
				for _, m := range members {
					if m.Input == nil {
						t.Fatal("unattempted member omitted its full input")
					}
				}
				if partial {
					actual := read()
					recovery := execute("undo", "--reconcile", rows[0].ID, "--apply")
					if !bytes.Equal(actual, read()) || !strings.Contains(recovery, `"partial":true`) {
						t.Fatal("recovery replayed target or hid partial result", recovery)
					}
				}
				execute("undo")
				var original, restored struct{ Issues map[string]map[string]any }
				if json.Unmarshal(before, &original) != nil || json.Unmarshal(read(), &restored) != nil {
					t.Fatal("invalid fixture state")
				}
				for _, id := range []string{"6", "7", "8"} {
					for _, field := range []string{"title", "body", "state", "labels"} {
						if !reflect.DeepEqual(original.Issues[id][field], restored.Issues[id][field]) {
							t.Fatal("inverse lost original task/review/visibility", id, field)
						}
					}
				}
				if original.Issues["8"]["updated_at"] != restored.Issues["8"]["updated_at"] {
					t.Fatal("declared no-op was written")
				}
			})
		}
	}
}

func TestGitHubJournalCLIPlannedCommandsValidateAllTargetsBeforeWrites(t *testing.T) {
	_, _, run := journalCLI(t)
	execute := func(args ...string) {
		t.Helper()
		if out, stderr, err := run(append(args, "--json")...); err != nil {
			t.Fatal(args, out, stderr, err)
		}
	}
	execute("create", "Valid review target", "--minor")
	execute("create", "Invalid closed review target", "--minor")
	execute("review", "gh-6", "--reason", "Review fixture")
	execute("close", "gh-7", "--reason", "Closed fixture")
	read := func(file string) []byte {
		t.Helper()
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	state, journal := os.Getenv("TD_SCHEDULE_STATE"), os.Getenv("TD_GH_FIXTURE_JOURNAL")
	before, history := read(state), read(journal)
	for _, args := range [][]string{
		{"reject", "gh-6", "gh-7"},
		{"delete", "gh-6", "6"},
		{"restore", "gh-6", "gh-999"},
	} {
		if out, stderr, err := run(append(args, "--reason", "Invalid batch", "--json")...); err == nil {
			t.Fatal("invalid late target succeeded", out, stderr)
		}
		if !bytes.Equal(before, read(state)) || !bytes.Equal(history, read(journal)) {
			t.Fatal("invalid late target allowed an earlier write", args)
		}
	}
}

func TestGitHubJournalCLIPlannedNoopRetainsNativeEventFence(t *testing.T) {
	_, _, run := journalCLI(t)
	t.Setenv("TD_GH_FIXTURE_NATIVE_EVENTS", "1")
	execute := func(args ...string) {
		t.Helper()
		if out, stderr, err := run(append(args, "--json")...); err != nil {
			t.Fatal(args, out, stderr, err)
		}
	}
	for _, title := range []string{"First deleted", "Second deleted", "Reviewed live no-op"} {
		execute("create", title, "--minor")
	}
	execute("review", "gh-8", "--reason", "No-op checkpoint")
	execute("delete", "gh-6", "gh-7", "--reason", "Restore fixture")
	execute("restore", "gh-6", "gh-7", "gh-8")
	last, stderr, err := run("last", "--n", "1", "--json")
	var rows []struct{ ID string }
	if err != nil || json.Unmarshal([]byte(last), &rows) != nil || len(rows) != 1 {
		t.Fatal(last, stderr, err)
	}
	file := os.Getenv("TD_GH_FIXTURE_JOURNAL")
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var journal map[string]any
	if err := json.Unmarshal(data, &journal); err != nil {
		t.Fatal(err)
	}
	// A native close/reopen can leave the same issue contents and server clock.
	// Its event history must still invalidate the originally watched no-op.
	journal["native_events"].(map[string]any)["8"] = []map[string]any{{"id": 1, "event": "closed"}, {"id": 2, "event": "reopened"}}
	data, err = json.Marshal(journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	state, err := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"undo", "--reconcile", rows[0].ID, "--apply", "--json"}, {"undo", "--json"}} {
		out, stderr, err := run(args...)
		if err == nil || !strings.Contains(out+stderr, "native events changed") {
			t.Fatal("no-op native event change allowed recovery/inverse or produced unrelated error", err, out, stderr)
		}
		after, _ := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
		latest, _ := os.ReadFile(file)
		if !bytes.Equal(state, after) || !bytes.Equal(data, latest) {
			t.Fatal("changed no-op allowed a partial inverse or journal write")
		}
	}
}

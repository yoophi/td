package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Reproduce #84 through the real CLI writers and inverse dispatch, without
// GitHub requests. Fixture clocks must advance even when fields are restored.
func TestGitHubJournalCLISequentialStartUpdateCommentsUndo(t *testing.T) {
	_, _, run := journalCLI(t)
	useHandoffReviewFixture(t)
	execute := func(args ...string) {
		t.Helper()
		out, stderr, err := run(append(args, "--json")...)
		if err != nil {
			t.Fatal(args, out, stderr, err)
		}
	}
	read := func() map[string]map[string]any {
		t.Helper()
		data, err := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
		if err != nil {
			t.Fatal(err)
		}
		var state struct{ Issues map[string]map[string]any }
		if err := json.Unmarshal(data, &state); err != nil {
			t.Fatal(err)
		}
		return state.Issues
	}
	execute("create", "First")
	execute("create", "Second")
	original := read()
	execute("start", "gh-6", "gh-7")
	started := read()
	execute("update", "gh-6", "gh-7", "--title", "Changed", "--points", "3", "--comment", "Companion")
	execute("undo")
	for id, issue := range started {
		if id != "6" && id != "7" {
			continue
		}
		for _, field := range []string{"title", "body", "state", "labels"} {
			if !reflect.DeepEqual(issue[field], read()[id][field]) {
				t.Fatalf("update inverse did not restore start observation: %s %s", id, field)
			}
		}
	}
	execute("undo") // Must undo the earlier start despite verified new clocks.
	for _, id := range []string{"6", "7"} {
		for _, field := range []string{"title", "body", "state", "labels"} {
			if !reflect.DeepEqual(original[id][field], read()[id][field]) {
				t.Fatalf("start inverse did not restore original: %s %s", id, field)
			}
		}
	}
}

func TestGitHubJournalCLISequentialGroupUndoRejectsUnprovenBridge(t *testing.T) {
	testSequentialUndoRejectsUnprovenBridge(t, false)
}

func testSequentialUndoRejectsUnprovenBridge(t *testing.T, paired bool, firstCommand ...[]string) {
	t.Helper()
	for _, mode := range []string{"foreign-title", "foreign-clock", "missing-effect", "foreign-author", "observed-inverse", "native-event"} {
		t.Run(mode, func(t *testing.T) {
			_, _, run := journalCLI(t)
			useHandoffReviewFixture(t)
			first := []string{"start", "gh-6", "gh-7"}
			if paired {
				first = []string{"update", "gh-6", "gh-7", "--title", "First command", "--comment", "First companion"}
			}
			if len(firstCommand) > 0 {
				first = firstCommand[0]
			}
			for _, args := range [][]string{{"create", "First"}, {"create", "Second"}, first, {"update", "gh-6", "gh-7", "--title", "Changed", "--comment", "Companion"}, {"undo"}} {
				if out, stderr, err := run(append(args, "--json")...); err != nil {
					t.Fatal(args, out, stderr, err)
				}
			}
			stateFile, journalFile := os.Getenv("TD_SCHEDULE_STATE"), os.Getenv("TD_GH_FIXTURE_JOURNAL")
			file := journalFile
			if strings.HasPrefix(mode, "foreign-") && mode != "foreign-author" {
				file = stateFile
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
			case "foreign-title":
				native["issues"].(map[string]any)["6"].(map[string]any)["title"] = "Foreign edit"
			case "foreign-clock":
				issue := native["issues"].(map[string]any)["6"].(map[string]any)
				clock, err := time.Parse(time.RFC3339, issue["updated_at"].(string))
				if err != nil {
					t.Fatal(err)
				}
				issue["updated_at"] = clock.Add(time.Second).Format(time.RFC3339)
			case "missing-effect", "foreign-author", "observed-inverse":
				comments := native["comments"].([]any)
				found := false
				for i, row := range comments {
					comment := row.(map[string]any)
					body := comment["body"].(string)
					parts := strings.SplitN(body, "<!-- td:action:", 2)
					var journal struct{ Phase string }
					if len(parts) != 2 {
						continue
					}
					payload := strings.SplitN(parts[1], "\n", 2)
					if len(payload) != 2 || json.Unmarshal([]byte(strings.TrimSuffix(payload[1], "\n-->")), &journal) != nil || journal.Phase != "undone" {
						continue // Mutate the completed later command, not the selected earlier command.
					}
					if mode == "missing-effect" && strings.Contains(body, "<!-- td:action:v26\n") {
						native["comments"] = append(comments[:i], comments[i+1:]...)
						found = true
						break
					}
					if strings.Contains(body, "<!-- td:action:v25\n") {
						switch mode {
						case "foreign-author":
							comment["user"].(map[string]any)["login"] = "different-author"
						case "observed-inverse":
							comment["body"] = strings.ReplaceAll(body, "verified-group-receipts", "group-contents-observed")
						default:
							continue
						}
						found = true
						break
					}
				}
				if !found {
					t.Fatal("missing fixture mutation seam", mode)
				}
			case "native-event":
				// Native close/reopen events also advance the issue clock. This
				// ordinary start fixture has no review/event baseline; do not
				// invent a server event with an unchanged old issue observation.
				stateData := relationFixtureBytes(t, "TD_SCHEDULE_STATE")
				var state map[string]any
				if err := json.Unmarshal(stateData, &state); err != nil {
					t.Fatal(err)
				}
				issue := state["issues"].(map[string]any)["6"].(map[string]any)
				clock, err := time.Parse(time.RFC3339, issue["updated_at"].(string))
				if err != nil {
					t.Fatal(err)
				}
				issue["updated_at"] = clock.Add(time.Second).Format(time.RFC3339)
				stateData, err = json.Marshal(state)
				if err != nil || os.WriteFile(stateFile, stateData, 0600) != nil {
					t.Fatal("native clock fixture update failed", err)
				}
				gh, err := exec.LookPath("gh")
				if err != nil {
					t.Fatal(err)
				}
				script, err := os.ReadFile(gh)
				if err != nil || !bytes.Contains(script, []byte("elif '/events?' in path:data=[[]]")) {
					t.Fatal("missing native event fixture seam", err)
				}
				script = bytes.Replace(script, []byte("elif '/events?' in path:data=[[]]"), []byte("elif '/events?' in path:data=[[{'id':99999,'event':'closed'}]]"), 1)
				if err := os.WriteFile(gh, script, 0755); err != nil {
					t.Fatal(err)
				}
			}
			data, err = json.Marshal(native)
			if err != nil || os.WriteFile(file, data, 0600) != nil {
				t.Fatal("fixture mutation failed", err)
			}
			before := relationFixtureBytes(t, "TD_SCHEDULE_STATE")
			journals := relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")
			out, stderr, err := run("undo", "--json")
			if err == nil || !json.Valid([]byte(out)) || !bytes.Equal(before, relationFixtureBytes(t, "TD_SCHEDULE_STATE")) || !bytes.Equal(journals, relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")) {
				t.Fatal("unproven bridge caused writes", mode, out, stderr, err)
			}
		})
	}
}

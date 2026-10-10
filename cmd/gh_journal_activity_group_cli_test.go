package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/marcus/td/internal/ghstore"
)

func TestGitHubJournalCLIWorkSessionActivityGroup(t *testing.T) {
	for _, command := range []string{"log", "handoff"} {
		for _, partial := range []bool{false, true} {
			name := command + "/complete"
			if partial {
				name = command + "/partial"
			}
			t.Run(name, func(t *testing.T) {
				_, _, run := journalCLI(t)
				useNativeCommentOrderFixture(t)
				execute := func(args ...string) string {
					t.Helper()
					out, stderr, err := run(append(args, "--json")...)
					if err != nil {
						t.Fatal(args, out, stderr, err)
					}
					return out
				}
				execute("create", "First activity target")
				execute("create", "Second activity target")
				execute("ws", "start", "Activity group fixture")
				execute("ws", "tag", "gh-6", "gh-7", "--no-start")
				args := []string{"ws", command, "Shared activity"}
				if command == "handoff" {
					args = []string{"ws", command, "--done", "Shared checkpoint", "--continue"}
				}
				if partial {
					t.Setenv("TD_GH_JOURNAL_FAIL", "receipt-denied")
				}
				out, stderr, err := run(append(args, "--json")...)
				if (err != nil) != partial {
					t.Fatal(out, stderr, err)
				}
				t.Setenv("TD_GH_JOURNAL_FAIL", "")
				var rows []struct {
					ID    string
					Group *ghstore.ActionGroup
				}
				last := execute("last", "--n", "1")
				if json.Unmarshal([]byte(last), &rows) != nil || len(rows) != 1 || rows[0].Group == nil || rows[0].Group.Command != "ws-"+command || len(rows[0].Group.Members) != 2 {
					t.Fatal("work-session activity escaped the aggregate", last)
				}
				read := func() []byte {
					t.Helper()
					data, err := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
					if err != nil {
						t.Fatal(err)
					}
					return data
				}
				if partial {
					before := read()
					result := execute("undo", "--reconcile", rows[0].ID, "--apply")
					if !bytes.Equal(before, read()) || !strings.Contains(result, `"partial":true`) {
						t.Fatal("group recovery replayed an activity or hid partial outcome", result)
					}
				}
				execute("undo")
				var remote struct{ Comments []any }
				if json.Unmarshal(read(), &remote) != nil || len(remote.Comments) != 0 {
					t.Fatal("group undo did not remove every actual member comment")
				}
				execute("undo", "--reconcile", rows[0].ID, "--apply")
				current := execute("ws", "current")
				if !strings.Contains(current, "gh-6") || !strings.Contains(current, "gh-7") {
					t.Fatal("remote undo rewound device-local tags", current)
				}
			})
		}
	}
}

// Keep task clocks unchanged so a revision-only fence cannot pass this test.
// Inject an edit to another member at the write boundary, after the original
// group was read but before the next native activity POST or DELETE.
func TestGitHubJournalCLIActivityGroupFencesOtherNativeComments(t *testing.T) {
	for _, boundary := range []string{"forward", "inverse"} {
		t.Run(boundary, func(t *testing.T) {
			_, _, run := journalCLI(t)
			useNativeCommentOrderFixture(t)
			execute := func(args ...string) {
				t.Helper()
				out, stderr, err := run(append(args, "--json")...)
				if err != nil {
					t.Fatal(args, out, stderr, err)
				}
			}
			execute("create", "First activity target")
			execute("create", "Second activity target")
			execute("ws", "start", "Activity fence fixture")
			execute("ws", "tag", "gh-6", "gh-7", "--no-start")
			if boundary == "inverse" {
				execute("ws", "log", "Shared activity")
			}
			gh, err := exec.LookPath("gh")
			if err != nil {
				t.Fatal(err)
			}
			script, err := os.ReadFile(gh)
			if err != nil {
				t.Fatal(err)
			}
			needle := "   data.update(payload);data['updated_at']=fixture_tick(data['updated_at'])"
			condition := `'"action":"comment_create"' in payload.get('body','') and '"phase":"undo_prepared"' in payload.get('body','')`
			indent := "   "
			if boundary == "forward" {
				// Edit the earlier native result when the second intent is
				// saved, after the first member finished all point readbacks.
				needle = " data['updated_at']=data['created_at'];journal['comments'].append(data);journal_file.write_text(json.dumps(journal))"
				condition = `'"action":"comment_create"' in body and len(json.loads(pathlib.Path(os.environ['TD_SCHEDULE_STATE']).read_text())['comments']) == 1`
				indent = " "
			}
			injection := needle + "\n" + indent + "if " + condition + " and not journal.get('activity_edit_injected'):\n" + indent + " fixture_path=pathlib.Path(os.environ['TD_SCHEDULE_STATE']);fixture_state=json.loads(fixture_path.read_text());fixture_state['comments'][0]['body']=fixture_state['comments'][0]['body'].replace('Shared activity','External change');fixture_path.write_text(json.dumps(fixture_state));journal['activity_edit_injected']=True"

			if !bytes.Contains(script, []byte(needle)) {
				t.Fatal("fixture receipt boundary missing")
			}
			if err := os.WriteFile(gh, bytes.Replace(script, []byte(needle), []byte(injection), 1), 0755); err != nil {
				t.Fatal(err)
			}
			args := []string{"ws", "log", "Shared activity", "--json"}
			want := 1
			if boundary == "inverse" {
				args, want = []string{"undo", "--json"}, 2
			}
			out, stderr, err := run(args...)
			if err == nil {
				t.Fatal("changed other-member comment was accepted", out, stderr)
			}
			data, err := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
			if err != nil {
				t.Fatal(err)
			}
			var remote struct{ Comments []struct{ Body string } }
			if json.Unmarshal(data, &remote) != nil || len(remote.Comments) != want || !strings.Contains(remote.Comments[0].Body, "External change") {
				t.Fatal("native write continued after another member changed", out, stderr)
			}
		})
	}
}

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

func TestGitHubJournalCLIClaimSweepAndWorkSessionGroups(t *testing.T) {
	for _, command := range []string{"claim-sweep", "ws-tag"} {
		for _, partial := range []bool{false, true} {
			name := command + "/complete"
			if partial {
				name = command + "/receipt-denied"
			}
			t.Run(name, func(t *testing.T) {
				dir, actor, run := journalCLI(t)
				execute := func(args ...string) string {
					t.Helper()
					out, stderr, err := run(append(args, "--json")...)
					if err != nil {
						t.Fatal(args, out, stderr, err)
					}
					return out
				}
				execute("create", "Fanout first")
				execute("create", "Fanout second")
				args := []string{"ws", "tag", "gh-6", "gh-7"}
				wantCommand := "start"
				if command == "claim-sweep" {
					execute("start", "gh-6", "gh-7")
					args = []string{"unstart", "--session", actor, "--force"}
					wantCommand = "unstart"
				} else {
					execute("ws", "start", "Fanout fixture")
				}
				before, err := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
				if err != nil {
					t.Fatal(err)
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
					ID, Phase string
					Group     *ghstore.ActionGroup
				}
				last := execute("last", "--n", "1")
				if json.Unmarshal([]byte(last), &rows) != nil || len(rows) != 1 || rows[0].Group == nil || rows[0].Group.Command != wantCommand || len(rows[0].Group.Members) != 2 {
					t.Fatal("fanout escaped command group", last)
				}
				if partial {
					actual, _ := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
					recovery := execute("undo", "--reconcile", rows[0].ID, "--apply")
					recovered, _ := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
					if !bytes.Equal(actual, recovered) || !strings.Contains(recovery, `"partial":true`) {
						t.Fatal("partial recovery replayed target or lost partial outcome", recovery)
					}
				}
				execute("undo")
				after, _ := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
				var original, restored struct{ Issues map[string]map[string]any }
				if json.Unmarshal(before, &original) != nil || json.Unmarshal(after, &restored) != nil {
					t.Fatal("invalid native fixture")
				}
				for _, id := range []string{"6", "7"} {
					for _, field := range []string{"title", "body", "state", "labels"} {
						if !reflect.DeepEqual(original.Issues[id][field], restored.Issues[id][field]) {
							t.Fatal("group inverse did not restore original claim/task", id, field)
						}
					}
				}
				if command == "ws-tag" && !partial {
					current := execute("ws", "current")
					if !strings.Contains(current, "gh-6") || !strings.Contains(current, "gh-7") {
						t.Fatal("remote undo incorrectly removed device-local tags", current)
					}
				}
				if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
					t.Fatal("SQLite fallback", err)
				}
			})
		}
	}
}

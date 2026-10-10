package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/marcus/td/internal/ghstore"
)

// Compare restored task fields independently of the production decoder while
// preserving the forward and compensation audit expected by ordered groups.
func sameFixtureTaskContentsWithoutAudit(t *testing.T, left, right string) bool {
	t.Helper()
	decode := func(body string) (string, map[string]any) {
		t.Helper()
		marker := "<!-- td:issue:v1\n"
		start := strings.Index(body, marker)
		end := strings.LastIndex(body, "\n-->")
		if start < 0 || end < start {
			t.Fatal("fixture task metadata is missing")
		}
		var meta map[string]any
		if err := json.Unmarshal([]byte(body[start+len(marker):end]), &meta); err != nil {
			t.Fatal(err)
		}
		delete(meta, "last_state_reason")
		if details, ok := meta["details"].(map[string]any); ok {
			for _, key := range []string{"transitions", "sessions", "reviews"} {
				delete(details, key)
			}
		}
		return body[:start], meta
	}
	lDescription, lMeta := decode(left)
	rDescription, rMeta := decode(right)
	return lDescription == rDescription && reflect.DeepEqual(lMeta, rMeta)
}

func TestGitHubJournalCLIOrderedUnblockThenExplicitClose(t *testing.T) {
	dir, _, run := journalCLI(t)
	t.Setenv("TD_GH_FIXTURE_NATIVE_EVENTS", "1")
	execute := func(args ...string) string {
		t.Helper()
		out, stderr, err := run(append(args, "--json")...)
		if err != nil {
			t.Fatal(args, out, stderr, err)
		}
		return out
	}
	execute("create", "First explicit root", "--minor")
	execute("create", "Blocked explicit root", "--minor")
	execute("update", "gh-7", "--depends-on", "gh-6")
	execute("block", "gh-7")
	execute("close", "gh-6", "gh-7", "--reason", "Synthetic compound closure")
	var rows []ghstore.ActionJournalRecord
	last := execute("last", "--n", "1")
	if err := json.Unmarshal([]byte(last), &rows); err != nil || len(rows) != 1 || rows[0].Group == nil || rows[0].Group.Ordered == nil {
		t.Fatal(last, err)
	}
	group := rows[0]
	effects := group.Group.Ordered.Forward
	if group.Phase != "applied" || len(effects) != 3 || effects[0].Record.IssueID != "gh-6" || effects[1].Record.IssueID != "gh-7" || effects[2].Record.IssueID != "gh-7" || effects[2].Record.GroupPreviousReceiptID != effects[1].Record.ID || effects[2].Record.GroupSequence != 3 {
		t.Fatal("actual compound sequence was not recorded", last)
	}
	if effects[1].Record.After.Revision != effects[2].Record.Before.Revision {
		t.Fatal("second target write did not use actual predecessor result")
	}
	inverse := execute("undo")
	if !strings.Contains(inverse, `"action":"undone"`) || !strings.Contains(inverse, `"target_write_verified":true`) {
		t.Fatal(inverse)
	}
	if completed := execute("last", "--n", "1"); json.Unmarshal([]byte(completed), &rows) != nil || rows[0].Phase != "undone" || len(rows[0].Group.Ordered.Steps) != 3 {
		t.Fatal("actual compound inverse receipt incomplete", completed)
	}
	if shown := execute("show", "gh-7"); !strings.Contains(shown, `"status":"blocked"`) {
		t.Fatal("former blocked status lost", shown)
	}
	before, _ := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
	execute("undo", "--reconcile", group.ID, "--apply")
	after, _ := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
	if string(before) != string(after) {
		t.Fatal("completed compound confirmation replayed targets")
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("SQLite fallback", err)
	}
}

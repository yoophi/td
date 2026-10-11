package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/marcus/td/internal/ghstore"
)

// Two selected sources update the same reverse-dependency target. Its combined
// write must retain both source edges, and one undo must restore all
// three tasks and remove only this command's inline comments.
func TestGitHubJournalCLIUpdateRelationsSharedTargetWholeCommand(t *testing.T) {
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
	for _, title := range []string{"First source", "Second source", "Shared blocked target"} {
		execute("create", title)
	}
	execute("log", "gh-8", "Unrelated target activity")
	before := read()
	execute("update", "gh-6", "gh-7", "--priority", "P1", "--blocks", "gh-8", "--comment", "Command companion")
	var current struct {
		Issues map[string]struct{ Body string }
	}
	if err := json.Unmarshal(read(), &current); err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(current.Issues["8"].Body, "<!-- td:issue:v1\n", 2)
	var meta struct {
		Details struct{ Dependencies []string }
	}
	if len(parts) != 2 || json.Unmarshal([]byte(strings.TrimSuffix(parts[1], "\n-->")), &meta) != nil || !slices.Equal(meta.Details.Dependencies, []string{"gh-6", "gh-7"}) {
		t.Fatal("shared target lost an earlier selected source", current.Issues["8"].Body)
	}
	var rows []struct {
		ID    string
		Group *ghstore.ActionGroup
	}
	last := execute("last", "--n", "1")
	if json.Unmarshal([]byte(last), &rows) != nil || len(rows) != 1 || rows[0].Group == nil {
		t.Fatal("mixed relationship update is not one command", last)
	}
	members := []string{}
	for _, member := range rows[0].Group.Members {
		members = append(members, member.IssueID)
	}
	slices.Sort(members)
	if !slices.Equal(members, []string{"gh-6", "gh-7", "gh-8"}) {
		t.Fatal("command omitted its shared relationship target", members)
	}
	writes := 0
	for _, a := range fixtureAllJournals(t) {
		if a.GroupContract == "update-relations" && a.IssueID == "gh-8" && a.Action == "update" {
			writes++
		}
	}
	if writes != 1 {
		t.Fatal("shared target did not receive one combined effect", writes)
	}
	execute("undo")
	var original, restored struct {
		Issues   map[string]map[string]any
		Comments []any
	}
	if json.Unmarshal(before, &original) != nil || json.Unmarshal(read(), &restored) != nil {
		t.Fatal("invalid fixture")
	}
	for _, id := range []string{"6", "7", "8"} {
		for _, field := range []string{"title", "body", "labels", "state"} {
			if !reflect.DeepEqual(original.Issues[id][field], restored.Issues[id][field]) {
				t.Fatal("command inverse lost original target contents", id, field)
			}
		}
	}
	if !reflect.DeepEqual(original.Comments, restored.Comments) {
		t.Fatal("command inverse changed unrelated activity or retained inline comments")
	}
	actual := read()
	execute("undo", "--reconcile", rows[0].ID, "--apply")
	if !bytes.Equal(actual, read()) {
		t.Fatal("completed mixed-command confirmation repeated target writes")
	}
}

func relationCLI(t *testing.T) (func(...string) (string, string, error), func(...string) string) {
	t.Helper()
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
	for _, title := range []string{"First", "Second", "Shared", "Watched", "Unrelated"} {
		execute("create", title)
	}
	return run, execute
}

func relationFixtureBytes(t *testing.T, env string) []byte {
	t.Helper()
	data, err := os.ReadFile(os.Getenv(env))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func assertRelationRestored(t *testing.T, before []byte) {
	t.Helper()
	var original, restored struct {
		Issues   map[string]map[string]any
		Comments []any
	}
	if json.Unmarshal(before, &original) != nil || json.Unmarshal(relationFixtureBytes(t, "TD_SCHEDULE_STATE"), &restored) != nil {
		t.Fatal("invalid fixture")
	}
	for _, id := range []string{"6", "7", "8", "9", "10"} {
		for _, field := range []string{"title", "body", "labels", "state"} {
			if !reflect.DeepEqual(original.Issues[id][field], restored.Issues[id][field]) {
				t.Fatal("lost original", id, field)
			}
		}
	}
	if !reflect.DeepEqual(original.Comments, restored.Comments) {
		t.Fatal("unrelated activity changed or companion retained")
	}
}

func TestGitHubJournalCLIUpdateRelationsRecovery(t *testing.T) {
	for _, failure := range []string{"late-target-lost", "comment-denied", "inverse-receipt-denied", "inverse-delete-lost"} {
		t.Run(failure, func(t *testing.T) {
			run, execute := relationCLI(t)
			execute("log", "gh-8", "Unrelated activity")
			before := relationFixtureBytes(t, "TD_SCHEDULE_STATE")
			switch failure {
			case "late-target-lost":
				file, err := exec.LookPath("gh")
				if err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				needle := "if os.environ.get('TD_GH_JOURNAL_FAIL')=='target-lost':"
				if strings.Count(string(data), needle) != 1 {
					t.Fatal("target failure seam changed")
				}
				body := strings.Replace(string(data), needle, "if os.environ.get('TD_GH_JOURNAL_FAIL')=='late-target-lost' and n=='8':", 1)
				if err := os.WriteFile(file, []byte(body), 0755); err != nil {
					t.Fatal(err)
				}
				t.Setenv("TD_GH_JOURNAL_FAIL", failure)
			case "comment-denied":
				t.Setenv("TD_SCHEDULE_FAIL_LOG", "1")
			}
			out, stderr, err := run("update", "gh-6", "gh-7", "--priority", "P1", "--blocks", "gh-8", "--comment", "Companion", "--json")
			forwardFailed := failure == "late-target-lost" || failure == "comment-denied"
			if (err != nil) != forwardFailed {
				t.Fatal(out, stderr, err)
			}
			if forwardFailed && !strings.Contains(out+stderr, "group ghc-") {
				t.Fatal("partial error lost command identity", out, stderr)
			}
			t.Setenv("TD_GH_JOURNAL_FAIL", "")
			t.Setenv("TD_SCHEDULE_FAIL_LOG", "")
			var rows []struct {
				ID    string
				Group *ghstore.ActionGroup
			}
			last := execute("last", "--n", "1")
			if json.Unmarshal([]byte(last), &rows) != nil || len(rows) != 1 || rows[0].Group == nil || rows[0].Group.RelationUpdates == nil {
				t.Fatal("missing relation group", last)
			}
			if forwardFailed {
				actual := relationFixtureBytes(t, "TD_SCHEDULE_STATE")
				execute("undo", "--reconcile", rows[0].ID, "--apply")
				if !bytes.Equal(actual, relationFixtureBytes(t, "TD_SCHEDULE_STATE")) {
					t.Fatal("forward recovery replayed targets")
				}
			}
			if !forwardFailed {
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
				actual := relationFixtureBytes(t, "TD_SCHEDULE_STATE")
				execute("undo", "--reconcile", rows[0].ID, "--apply")
				if !bytes.Equal(actual, relationFixtureBytes(t, "TD_SCHEDULE_STATE")) {
					t.Fatal("inverse recovery replayed targets")
				}
			}
			execute("undo")
			assertRelationRestored(t, before)
			actual := relationFixtureBytes(t, "TD_SCHEDULE_STATE")
			execute("undo", "--reconcile", rows[0].ID, "--apply")
			if !bytes.Equal(actual, relationFixtureBytes(t, "TD_SCHEDULE_STATE")) {
				t.Fatal("completed confirmation wrote targets")
			}
			if failure != "comment-denied" {
				for _, a := range fixtureAllJournals(t) {
					if a.Group != nil && a.Group.RelationUpdates != nil && a.Group.Inverse.Proof != "group-contents-observed" {
						t.Fatal("weak proof promoted", a.Group.Inverse)
					}
				}
			}
		})
	}
}

func TestGitHubJournalCLIUpdateRelationsOrderAndDetails(t *testing.T) {
	for _, scenario := range []string{"parent-only", "dependencies-and-details", "reverse-order", "reverse-only", "unchanged"} {
		t.Run(scenario, func(t *testing.T) {
			_, execute := relationCLI(t)
			if scenario == "reverse-order" {
				execute("dep", "add", "gh-7", "gh-6")
			}
			before := relationFixtureBytes(t, "TD_SCHEDULE_STATE")
			journal := relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")
			switch scenario {
			case "parent-only":
				execute("update", "gh-6", "--parent", "gh-8")
			case "dependencies-and-details":
				execute("update", "gh-6", "--depends-on", "gh-7", "--parent", "gh-8", "--sprint", "Release candidate", "--due", "2026-12-01", "--append", "--description", "Extra", "--comment", "Companion")
			case "reverse-order":
				execute("update", "gh-6", "--depends-on", "gh-7", "--blocks", "", "--comment", "Companion")
			case "reverse-only":
				execute("update", "gh-6", "--blocks", "gh-8")
			case "unchanged":
				execute("update", "gh-6", "--depends-on", "")
			}
			if scenario == "unchanged" {
				if !bytes.Equal(before, relationFixtureBytes(t, "TD_SCHEDULE_STATE")) || !bytes.Equal(journal, relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")) {
					t.Fatal("no-op wrote task or journal")
				}
				return
			}
			if scenario == "reverse-order" {
				found := false
				for _, a := range fixtureAllJournals(t) {
					if a.GroupContract == "update-relations" && a.GroupSequence == 1 {
						found = a.IssueID == "gh-7"
					}
				}
				if !found {
					t.Fatal("did not remove opposite edge first")
				}
			}
			if scenario == "dependencies-and-details" {
				var task ghstore.Record
				if json.Unmarshal([]byte(execute("show", "gh-6")), &task) != nil || task.ParentID != "gh-8" || task.Sprint != "Release candidate" || task.Description != "Extra" || task.DueDate == nil || *task.DueDate != "2026-12-01" || task.Details == nil || !slices.Equal(task.Details.Dependencies, []string{"gh-7"}) {
					t.Fatal("combined details were not applied", task)
				}
			}
			execute("undo")
			assertRelationRestored(t, before)
		})
	}
}

func TestGitHubJournalCLIUpdateRelationsForeignGraph(t *testing.T) {
	for _, mode := range []string{"shared-target", "watched-target", "new-dependent", "unrelated"} {
		t.Run(mode, func(t *testing.T) {
			run, execute := relationCLI(t)
			execute("update", "gh-6", "gh-7", "--depends-on", "gh-9", "--blocks", "gh-8", "--comment", "Companion")
			var native struct {
				Issues   map[string]map[string]any `json:"issues"`
				Comments []any                     `json:"comments"`
			}
			if err := json.Unmarshal(relationFixtureBytes(t, "TD_SCHEDULE_STATE"), &native); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "shared-target":
				native.Issues["8"]["title"] = "Foreign same-clock edit"
			case "watched-target":
				native.Issues["9"]["title"] = "Foreign graph boundary edit"
			case "unrelated":
				native.Issues["10"]["title"] = "Allowed unrelated edit"
			case "new-dependent":
				body := native.Issues["10"]["body"].(string)
				parts := strings.SplitN(body, "<!-- td:issue:v1\n", 2)
				var meta map[string]any
				if len(parts) != 2 || json.Unmarshal([]byte(strings.TrimSuffix(parts[1], "\n-->")), &meta) != nil {
					t.Fatal("invalid task metadata")
				}
				meta["details"].(map[string]any)["dependencies"] = []string{"gh-6"}
				encoded, err := json.Marshal(meta)
				if err != nil {
					t.Fatal(err)
				}
				native.Issues["10"]["body"] = parts[0] + "<!-- td:issue:v1\n" + string(encoded) + "\n-->"
			}
			data, err := json.Marshal(native)
			if err != nil || os.WriteFile(os.Getenv("TD_SCHEDULE_STATE"), data, 0600) != nil {
				t.Fatal("fixture edit", err)
			}
			journal := relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")
			out, stderr, err := run("undo", "--json")
			if mode == "unrelated" {
				if err != nil {
					t.Fatal("unrelated edit blocked undo", out, stderr, err)
				}
				var after struct{ Issues map[string]map[string]any }
				if json.Unmarshal(relationFixtureBytes(t, "TD_SCHEDULE_STATE"), &after) != nil || after.Issues["10"]["title"] != "Allowed unrelated edit" {
					t.Fatal("lost unrelated edit")
				}
				return
			}
			if err == nil || !strings.Contains(out+stderr, "changed") && !strings.Contains(out+stderr, "conflict") {
				t.Fatal("foreign graph accepted", out, stderr, err)
			}
			if !bytes.Equal(data, relationFixtureBytes(t, "TD_SCHEDULE_STATE")) || !bytes.Equal(journal, relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")) {
				t.Fatal("conflict attempted inverse writes")
			}
		})
	}
}

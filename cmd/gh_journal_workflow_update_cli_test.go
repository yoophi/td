package cmd

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/ghstore"
)

// A status request is part of the same update command as its fields, shared
// reverse dependencies and companion comments. Review/native transitions must
// retain their policy and compensation audit while still exposing one undo.
func TestGitHubJournalCLIWorkflowUpdateWholeCommand(t *testing.T) {
	for _, scenario := range []struct {
		name, setup, status string
	}{
		{"start", "", "in_progress"},
		{"start-unchanged", "start", "in_progress"},
		{"review", "", "in_review"},
		{"review-auto-handoff", "", "in_review"},
		{"review-existing-handoff", "", "in_review"},
		{"reject", "review", "open"},
		{"close", "", "closed"},
		{"reopen", "close", "open"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			// Each synthetic gh request starts a subprocess. The whole-command
			// inverse includes automatic handoffs as well as task/comment effects.
			_, _, run := journalCLIWithTimeout(t, 2*time.Minute)
			useNativeCommentOrderFixture(t)
			useHandoffReviewFixture(t)
			t.Setenv("TD_GH_FIXTURE_NATIVE_EVENTS", "1")
			execute := func(args ...string) string {
				t.Helper()
				started := time.Now()
				out, stderr, err := run(append(args, "--json")...)
				t.Logf("%s took %s", args[0], time.Since(started).Round(time.Millisecond))
				if err != nil {
					t.Fatal(args, out, stderr, err)
				}
				return out
			}
			for _, title := range []string{"First selected task", "Second selected task", "Shared reverse target"} {
				args := []string{"create", title}
				if scenario.name != "review-auto-handoff" && scenario.name != "review-existing-handoff" {
					args = append(args, "--minor")
				}
				execute(args...)
			}
			if scenario.setup != "" {
				if scenario.setup == "start" {
					execute("start", "gh-6", "gh-7")
				} else {
					execute(scenario.setup, "gh-6", "gh-7", "--reason", "Prepare workflow fixture")
				}
			}
			if scenario.name == "review-existing-handoff" {
				execute("handoff", "gh-6", "--done", "Existing first handoff")
				execute("handoff", "gh-7", "--done", "Existing second handoff")
			}
			execute("log", "gh-8", "Unrelated activity survives command undo")
			before := relationFixtureBytes(t, "TD_SCHEDULE_STATE")
			execute("update", "gh-6", "gh-7", "--priority", "P1", "--blocks", "gh-8", "--status", scenario.status, "--comment", "Whole workflow command")
			for _, id := range []string{"gh-6", "gh-7"} {
				var actual struct{ Status, Priority string }
				out := execute("show", id)
				if err := json.Unmarshal([]byte(out), &actual); err != nil || actual.Status != scenario.status || actual.Priority != "P1" {
					t.Fatal("mixed command did not apply requested fields and workflow", id, out, err)
				}
			}
			var shared struct {
				Details struct{ Dependencies []string }
			}
			if out := execute("show", "gh-8"); json.Unmarshal([]byte(out), &shared) != nil || !slices.Equal(shared.Details.Dependencies, []string{"gh-6", "gh-7"}) {
				t.Fatal("shared target lost a selected source", out)
			}
			var rows []ghstore.ActionJournalRecord
			last := execute("last", "--n", "1")
			if err := json.Unmarshal([]byte(last), &rows); err != nil || len(rows) != 1 || rows[0].Group == nil {
				t.Fatal("workflow update is not one command", err)
			}
			members := []string{}
			for _, member := range rows[0].Group.Members {
				members = append(members, member.IssueID)
			}
			for _, id := range []string{"gh-6", "gh-7", "gh-8"} {
				if !slices.Contains(members, id) {
					t.Fatal("command omitted an actual target", id, members)
				}
			}
			execute("undo")
			var original, restored struct {
				Issues   map[string]map[string]any
				Comments []any
			}
			if json.Unmarshal(before, &original) != nil || json.Unmarshal(relationFixtureBytes(t, "TD_SCHEDULE_STATE"), &restored) != nil {
				t.Fatal("invalid fixture")
			}
			for _, id := range []string{"6", "7", "8"} {
				for _, field := range []string{"title", "labels", "state"} {
					if !reflect.DeepEqual(original.Issues[id][field], restored.Issues[id][field]) {
						t.Fatal("whole-command inverse lost original target contents", id, field)
					}
				}
				originalBody := fixtureWorkflowBodyWithoutReview(t, original.Issues[id]["body"].(string), false)
				restoredBody := fixtureWorkflowBodyWithoutReview(t, restored.Issues[id]["body"].(string), true)
				if !sameFixtureTaskContentsWithoutAudit(t, originalBody, restoredBody) {
					t.Fatal("whole-command inverse lost original task fields", id)
				}
			}
			if !reflect.DeepEqual(original.Comments, restored.Comments) {
				t.Fatal("whole-command inverse changed unrelated activity or retained a companion")
			}
			actual := relationFixtureBytes(t, "TD_SCHEDULE_STATE")
			execute("undo", "--reconcile", rows[0].ID, "--apply")
			if !bytes.Equal(actual, relationFixtureBytes(t, "TD_SCHEDULE_STATE")) {
				t.Fatal("completed workflow confirmation repeated target writes")
			}
		})
	}
}

// Compensation restores task fields and claims, but never revives an approval
// or a review checkpoint for the old contents. Assert that distinction without
// using the production metadata decoder or ignoring unrelated task fields.
func fixtureWorkflowBodyWithoutReview(t *testing.T, body string, requireCleared bool) string {
	t.Helper()
	const marker = "<!-- td:issue:v1\n"
	start, end := strings.Index(body, marker), strings.LastIndex(body, "\n-->")
	if start < 0 || end < start {
		t.Fatal("fixture task metadata is missing")
	}
	var meta map[string]any
	if err := json.Unmarshal([]byte(body[start+len(marker):end]), &meta); err != nil {
		t.Fatal(err)
	}
	if details, ok := meta["details"].(map[string]any); ok {
		for _, key := range []string{"review_basis", "review_events", "review_requested_by_session", "review_handoff_id", "review_handoff_updated_at", "reviewer_session", "reviewed_at"} {
			if value, found := details[key]; requireCleared && found && value != nil && value != "" {
				t.Fatalf("workflow inverse revived historical review field %s: %v", key, value)
			}
			delete(details, key)
		}
		if requireCleared {
			if reviews, ok := details["reviews"].([]any); ok {
				for _, raw := range reviews {
					review := raw.(map[string]any)
					if review["decision"] == "approved" && review["superseded_at"] == nil {
						t.Fatal("workflow inverse revived an active approval")
					}
				}
			}
		}
	}
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	return body[:start+len(marker)] + string(data) + body[end:]
}

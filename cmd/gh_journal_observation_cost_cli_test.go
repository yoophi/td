package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Exercise the actual paired writer/inverse path. Adjacent event collection
// reads with no intervening observation or write expose repeated proof checks
// inside one validation boundary, rather than imposing a total API budget.
func TestGitHubJournalCLISequentialProofDoesNotRepeatAdjacentEventReads(t *testing.T) {
	for _, history := range []int{0, 1000} {
		t.Run(fmt.Sprintf("history-%d", history), func(t *testing.T) { testSequentialProofObservationCost(t, history) })
	}
}

func testSequentialProofObservationCost(t *testing.T, history int) {
	_, _, run := journalCLIWithTimeout(t, 2*time.Minute)
	useHandoffReviewFixture(t)
	calls := recordScheduleGHCalls(t)
	offset := 0
	execute := func(args ...string) {
		t.Helper()
		out, stderr, err := run(append(args, "--json")...)
		if err != nil {
			t.Fatal(args, out, stderr, err)
		}
		data, err := os.ReadFile(calls)
		if err != nil {
			t.Fatal(err)
		}
		rows := strings.Split(strings.TrimSpace(string(data)), "\n")
		events := 0
		for _, row := range rows[offset:] {
			if strings.Contains(row, "/events?per_page=") {
				events++
			}
		}
		t.Logf("phase=%v gh-invocations=%d native-event-invocations=%d", args, len(rows)-offset, events)
		offset = len(rows)

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

	if history > 0 {
		path := os.Getenv("TD_SCHEDULE_STATE")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var state map[string]any
		if err := json.Unmarshal(data, &state); err != nil {
			t.Fatal(err)
		}
		comments, _ := state["comments"].([]any)
		for i := 0; i < history; i++ {
			comments = append(comments, map[string]any{"id": 700000 + i, "body": fmt.Sprintf("Unrelated historical comment %d", i), "issue_url": "https://api.github.com/repos/owner/repo/issues/6", "created_at": "2025-01-01T00:00:00Z", "updated_at": "2025-01-01T00:00:00Z", "user": map[string]any{"login": "historical-author"}})
		}
		state["comments"] = comments
		data, err = json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	execute("start", "gh-6", "gh-7")
	execute("review", "gh-6", "gh-7")
	original := read()
	execute("update", "gh-6", "gh-7", "--points", "3", "--comment", "First companion")
	execute("update", "gh-6", "gh-7", "--points", "5", "--comment", "Later companion")
	execute("undo")
	execute("undo")
	restored := read()
	for _, id := range []string{"6", "7"} {
		for _, field := range []string{"title", "body", "state", "labels"} {
			if !reflect.DeepEqual(original[id][field], restored[id][field]) {
				t.Fatal("inverse lost original", id, field)
			}
		}
	}
	data, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	events, repeated := 0, 0
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var args []string
		if err := json.Unmarshal([]byte(line), &args); err != nil {
			t.Fatal(err)
		}
		endpoint := ""
		for _, arg := range args {
			if strings.Contains(arg, "/events?per_page=") {
				endpoint = arg
			}
		}
		if endpoint == "" {
			seen = map[string]bool{}
			continue
		}
		events++
		if seen[endpoint] {
			repeated++
		}
		seen[endpoint] = true
	}
	t.Logf("native event collection invocations=%d; repeated adjacent proof reads=%d", events, repeated)
	if events == 0 {
		t.Fatal("did not exercise native event proof path")
	}
	if repeated != 0 {
		t.Fatalf("same validation repeats %d native-event reads without another observation or write", repeated)
	}
}

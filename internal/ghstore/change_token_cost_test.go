package ghstore

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// Keep the cost of the complete observation visible: detecting edits/deletions
// in old comments requires the complete repository comment collection.
func TestChangeTokenRequestCost(t *testing.T) {
	issues := make([]apiIssue, 44)
	for i := range issues {
		issues[i] = apiIssue{Number: i + 1, State: "open"}
	}
	calls := 0
	c := &Client{repo: "owner/repo", run: func(_ context.Context, _ string, _ []byte, args ...string) ([]byte, error) {
		calls++
		if strings.Contains(args[5], "/comments?") {
			return []byte(`[[]]`), nil
		}
		return json.Marshal([][]apiIssue{issues})
	}}
	first, err := c.ChangeToken(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("first sweep: got %d gh calls, want 2", calls)
	}
	again, err := c.ChangeToken(context.Background())
	if err != nil || again != first || calls != 4 {
		t.Fatalf("unchanged sweep: calls=%d err=%v", calls, err)
	}
	t.Log("44 issues: 2 gh API calls per sweep, including unchanged sweeps; repository preflight adds one call")
}

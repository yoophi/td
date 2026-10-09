package ghstore

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/models"
)

func TestExportIssuesPaginationAuthorsDeletedAndCost(t *testing.T) {
	now := time.Now().UTC()
	body, err := encodeBody("deleted description", metadata{Type: models.TypeTask, Priority: models.PriorityP2, Details: &IssueDetails{DeletedAt: &now}})
	if err != nil {
		t.Fatal(err)
	}
	board, err := encodeBody("", metadata{Type: models.TypeTask, Priority: models.PriorityP2, EntityKind: "board", Board: &BoardDetails{Version: 1, ViewMode: "swimlanes"}})
	if err != nil {
		t.Fatal(err)
	}
	one := apiIssue{Number: 1, State: "open", Title: "native task", Body: "body"}
	one.User.Login = "original-author"
	pages := [][]apiIssue{{one, {Number: 2, State: "open", Body: board}}, {{Number: 3, State: "closed", Body: body}, {Number: 4, PullRequest: json.RawMessage(`{}`)}}}
	calls := []string{}
	failure := false
	c := &Client{repo: "owner/repo", run: func(_ context.Context, _ string, _ []byte, args ...string) ([]byte, error) {
		endpoint := args[5]
		calls = append(calls, endpoint)
		if strings.Contains(endpoint, "/comments?") {
			if failure {
				return nil, errors.New("HTTP 403: API rate limit exceeded")
			}
			return json.Marshal([][]apiComment{{{ID: 1, Body: "native comment", CreatedAt: now, UpdatedAt: now}}})
		}
		return json.Marshal(pages)
	}}
	rows, err := c.ExportIssues(context.Background(), true, true)
	if err != nil || len(rows) != 2 || rows[0].Author != "original-author" || rows[1].Record.DeletedAt == nil || rows[1].Body != body || len(rows[0].Activity) != 1 || len(calls) != 3 {
		t.Fatalf("rows=%+v calls=%v err=%v", rows, calls, err)
	}
	calls = nil
	rows, err = c.ExportIssues(context.Background(), false, false)
	if err != nil || len(rows) != 1 || len(rows[0].Activity) != 0 || !reflect.DeepEqual(calls, []string{"repos/owner/repo/issues?state=open&per_page=100"}) {
		t.Fatal(rows, calls, err)
	}
	failure = true
	if rows, err := c.ExportIssues(context.Background(), true, true); err == nil || rows != nil {
		t.Fatal("partial export returned", rows, err)
	}
	failure = false
	pages = append(pages, []apiIssue{one})
	if rows, err := c.ExportIssues(context.Background(), true, false); err == nil || rows != nil {
		t.Fatal("duplicate page accepted", rows, err)
	}
}

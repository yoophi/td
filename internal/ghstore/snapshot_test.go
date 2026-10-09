package ghstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func snapshotFixture(issues, comments any, commentErr error) (*Client, *[]string) {
	calls := []string{}
	c := &Client{repo: "owner/repo", run: func(_ context.Context, _ string, _ []byte, args ...string) ([]byte, error) {
		calls = append(calls, args[5])
		switch args[5] {
		case "repos/owner/repo/issues?state=all&per_page=100":
			return json.Marshal(issues)
		case "repos/owner/repo/issues/comments?per_page=100":
			if commentErr != nil {
				return nil, commentErr
			}
			return json.Marshal(comments)
		default:
			return nil, fmt.Errorf("unexpected per-issue request %s", args[5])
		}
	}}
	return c, &calls
}

func TestSnapshotBulkPaginationAndIssueOnlyScope(t *testing.T) {
	issues := [][]apiIssue{{}, {}}
	comments := [][]apiComment{{}, {}}
	for i := 1; i <= 145; i++ {
		page := 0
		if i > 100 {
			page = 1
		}
		issues[page] = append(issues[page], apiIssue{Number: i, State: "open"})
		comments[page] = append(comments[page], apiComment{ID: int64(i), IssueURL: fmt.Sprintf("https://api.github.com/repos/owner/repo/issues/%d", i), Body: "hello"})
	}
	c, calls := snapshotFixture(issues, comments, nil)
	s, err := c.ReadSnapshot(context.Background(), true)
	if err != nil || len(s.Issues(true)) != 145 || len(*calls) != 2 {
		t.Fatalf("s=%v calls=%v err=%v", s, *calls, err)
	}
	a, err := s.Activity("gh-145")
	if err != nil || len(a) != 1 || a[0].ID != "ghc-145" {
		t.Fatalf("%v %v", a, err)
	}
	// gh --paginate performs HTTP pagination inside these two invocations.
	s, err = c.ReadSnapshot(context.Background(), false)
	if err != nil || len(*calls) != 3 {
		t.Fatalf("issue-only: %v %v", *calls, err)
	}
	if _, err = s.Activity("gh-1"); err == nil {
		t.Fatal("partial scope exposed activity")
	}
	if _, err = s.ChangeToken(); err == nil {
		t.Fatal("partial scope exposed full token")
	}
}

func TestSnapshotFiltersPRCommentsBeforeMetadataDecode(t *testing.T) {
	c, _ := snapshotFixture([][]apiIssue{{{Number: 1, State: "open"}, {Number: 2, PullRequest: json.RawMessage(`{}`)}}}, [][]apiComment{{{ID: 1, IssueURL: "https://api.github.com/repos/owner/repo/issues/2", Body: activityPrefix + "malformed PR metadata"}}}, nil)
	s, err := c.ReadSnapshot(context.Background(), true)
	if err != nil || len(s.Issues(true)) != 1 {
		t.Fatalf("%v %v", s, err)
	}
	a, err := s.Activity("gh-1")
	if err != nil || len(a) != 0 {
		t.Fatalf("%v %v", a, err)
	}
}

func TestSnapshotFailsWithoutPublishingPartialObservation(t *testing.T) {
	body, err := renderActivity(activityData{Kind: "comment", SessionID: "ses-one", OperationID: "same", Message: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	good := apiComment{ID: 1, IssueURL: "https://api.github.com/repos/owner/repo/issues/1", Body: "hello"}
	cases := []struct {
		name     string
		comments [][]apiComment
		failure  error
	}{
		{"duplicate comment", [][]apiComment{{good}, {good}}, nil},
		{"missing issue", [][]apiComment{{{ID: 1, IssueURL: "https://api.github.com/repos/owner/repo/issues/999"}}}, nil},
		{"foreign repo", [][]apiComment{{{ID: 1, IssueURL: "https://api.github.com/repos/other/repo/issues/1"}}}, nil},
		{"missing url", [][]apiComment{{{ID: 1}}}, nil},
		{"malformed activity", [][]apiComment{{{ID: 1, IssueURL: good.IssueURL, Body: activityPrefix + "bad"}}}, nil},
		{"duplicate operation", [][]apiComment{{{ID: 1, IssueURL: good.IssueURL, Body: body}, {ID: 2, IssueURL: good.IssueURL, Body: body}}}, nil},
		{"null page", [][]apiComment{nil}, nil},
		{"request failure", nil, errors.New("HTTP 403 rate limit exceeded original diagnostic")},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := snapshotFixture([][]apiIssue{{{Number: 1, State: "open"}}}, tt.comments, tt.failure)
			s, err := c.ReadSnapshot(context.Background(), true)
			if err == nil || s != nil {
				t.Fatalf("partial snapshot=%v err=%v", s, err)
			}
			if tt.failure != nil && !strings.Contains(err.Error(), tt.failure.Error()) {
				t.Fatalf("original diagnostic lost: %v", err)
			}
		})
	}
}

func TestSnapshotEmptyRepositorySkipsCommentRead(t *testing.T) {
	c, calls := snapshotFixture([][]apiIssue{{}}, nil, nil)
	s, err := c.ReadSnapshot(context.Background(), true)
	if err != nil || s == nil || len(*calls) != 1 {
		t.Fatalf("%v %v %v", s, *calls, err)
	}
}

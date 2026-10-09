package ghstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type incrementalFixture struct {
	client                         *Client
	fullIssues, deltaIssues        [][]apiIssue
	fullComments, deltaComments    [][]apiComment
	deltaIssueErr, deltaCommentErr error
	deltaIssueRaw, deltaCommentRaw []byte
	calls                          []string
}

func newIncrementalFixture(t *testing.T) *incrementalFixture {
	t.Helper()
	old := time.Now().Add(-time.Hour).UTC()
	f := &incrementalFixture{
		fullIssues:   [][]apiIssue{{{Number: 1, State: "closed", Title: "original", UpdatedAt: old}, {Number: 2, State: "open", Title: "untouched", UpdatedAt: old}}},
		fullComments: [][]apiComment{{{ID: 1, IssueURL: "https://api.github.com/repos/owner/repo/issues/1", Body: "original", UpdatedAt: old}}},
		deltaIssues:  [][]apiIssue{{}}, deltaComments: [][]apiComment{{}},
	}
	f.client = &Client{repo: "owner/repo", cache: &snapshotCache{root: filepath.Join(t.TempDir(), "td", "gh-issue", "v1"), repo: "owner/repo", credential: cacheHash(t.Name())}}
	f.client.run = func(_ context.Context, _ string, _ []byte, args ...string) ([]byte, error) {
		endpoint := args[5]
		f.calls = append(f.calls, endpoint)
		if strings.Contains(endpoint, "/issues/comments?") {
			if strings.Contains(endpoint, "since=") {
				if f.deltaCommentErr != nil {
					return nil, f.deltaCommentErr
				}
				if f.deltaCommentRaw != nil {
					return f.deltaCommentRaw, nil
				}
				return json.Marshal(f.deltaComments)
			}
			return json.Marshal(f.fullComments)
		}
		if strings.Contains(endpoint, "/issues?state=all") {
			if strings.Contains(endpoint, "since=") {
				if f.deltaIssueErr != nil {
					return nil, f.deltaIssueErr
				}
				if f.deltaIssueRaw != nil {
					return f.deltaIssueRaw, nil
				}
				return json.Marshal(f.deltaIssues)
			}
			return json.Marshal(f.fullIssues)
		}
		return nil, fmt.Errorf("unexpected individual endpoint: %s", endpoint)
	}
	return f
}
func ageIncrementalCache(t *testing.T, c *Client, age, fullAge time.Duration) cacheEnvelope {
	t.Helper()
	path := c.cache.path(true)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var e cacheEnvelope
	if err := json.Unmarshal(data, &e); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	e.CollectedAt = now.Add(-age)
	e.FullReconciledAt = now.Add(-fullAge)
	e.Cursor = e.CollectedAt.Format(time.RFC3339Nano)
	data, err = json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return e
}
func TestIncrementalSnapshotMergeReopenEditPaginationOverlapAndCursor(t *testing.T) {
	f := newIncrementalFixture(t)
	c := f.client
	ctx := context.Background()
	first, err := c.ReadSnapshot(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	oldToken, _ := first.ChangeToken()
	before := ageIncrementalCache(t, c, 31*time.Second, time.Minute)
	now := time.Now().UTC()
	reopened := apiIssue{Number: 1, Title: "reopened", State: "open", UpdatedAt: now}
	added := apiIssue{Number: 3, Title: "new", State: "open", UpdatedAt: now}
	f.deltaIssues = [][]apiIssue{{reopened, added}, {added}}
	edited := apiComment{ID: 1, IssueURL: "https://api.github.com/repos/owner/repo/issues/1", Body: "edited", UpdatedAt: now}
	older := edited
	older.Body = "older page"
	older.UpdatedAt = now.Add(-time.Second)
	newComment := apiComment{ID: 2, IssueURL: "https://api.github.com/repos/owner/repo/issues/3", Body: "new comment", UpdatedAt: now}
	f.deltaComments = [][]apiComment{{edited}, {older, newComment, newComment}}
	f.calls = nil
	s, err := c.ReadSnapshot(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	byNumber := map[int]Record{}
	for _, r := range s.Issues(true) {
		byNumber[r.Number] = r
	}
	if len(byNumber) != 3 || byNumber[1].Title != "reopened" || byNumber[1].Status != "open" || byNumber[2].Title != "untouched" || byNumber[3].Title != "new" {
		t.Fatalf("merge failed: %+v", byNumber)
	}
	a, err := s.Activity("gh-1")
	if err != nil || len(a) != 1 || a[0].Message != "edited" {
		t.Fatalf("edited comment: %+v %v", a, err)
	}
	a, err = s.Activity("gh-3")
	if err != nil || len(a) != 1 {
		t.Fatalf("new comment: %+v %v", a, err)
	}
	if len(f.calls) != 2 || !strings.Contains(f.calls[0], "since=") || !strings.Contains(f.calls[1], "since=") {
		t.Fatal(f.calls)
	}
	for _, endpoint := range f.calls {
		u, err := url.Parse("https://api.github.com/" + endpoint)
		if err != nil || u.Query().Get("since") != before.CollectedAt.Add(-2*time.Second).UTC().Format(time.RFC3339) {
			t.Fatalf("cursor overlap changed: %s %v", endpoint, err)
		}
	}
	if !s.FullReconciledAt().Equal(before.FullReconciledAt) || !s.ObservedAt().After(before.CollectedAt) || s.ObservationMode() != "incremental" {
		t.Fatal("observation identity lost")
	}
	token, _ := s.ChangeToken()
	if token == oldToken {
		t.Fatal("changed data reused old token")
	}
	data, err := os.ReadFile(c.cache.path(true))
	if err != nil {
		t.Fatal(err)
	}
	var after cacheEnvelope
	if err := json.Unmarshal(data, &after); err != nil {
		t.Fatal(err)
	}
	if after.Cursor != s.ObservedAt().Format(time.RFC3339Nano) || after.Cursor == before.Cursor || !after.FullReconciledAt.Equal(before.FullReconciledAt) {
		t.Fatal("cursor did not advance atomically")
	}
	if _, err := c.ReadSnapshot(ctx, true); err != nil || len(f.calls) != 2 {
		t.Fatal("fresh delta cache missed", err, f.calls)
	}
	status, err := c.CacheStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.FullReconciliationSeconds != 300 || status.Snapshots[1].ObservationMode != "incremental" {
		t.Fatalf("status: %+v", status)
	}
}
func TestIncrementalPhysicalDeletionWaitsForFullReconciliationAndExportForcesFull(t *testing.T) {
	f := newIncrementalFixture(t)
	c := f.client
	ctx := context.Background()
	if _, err := c.ReadSnapshot(ctx, true); err != nil {
		t.Fatal(err)
	}
	ageIncrementalCache(t, c, 31*time.Second, time.Minute)
	f.fullIssues = [][]apiIssue{{{Number: 1, State: "open", Title: "remote", UpdatedAt: time.Now()}}}
	f.fullComments = [][]apiComment{{}}
	// since cannot report the deletion of issue 2 or comment 1.
	s, err := c.ReadSnapshot(ctx, true)
	if err != nil || len(s.Issues(true)) != 2 {
		t.Fatal("baseline unexpectedly lost deleted entity", err)
	}
	a, _ := s.Activity("gh-1")
	if len(a) != 1 {
		t.Fatal("invented comment tombstone")
	}
	f.calls = nil
	exported, err := c.ExportIssues(ctx, true, true)
	if err != nil || len(exported) != 1 || len(exported[0].Activity) != 0 || len(f.calls) != 2 || strings.Contains(f.calls[0], "since=") {
		t.Fatalf("export used delta/cache: %+v %v %v", exported, err, f.calls)
	}
	ageIncrementalCache(t, c, 31*time.Second, 6*time.Minute)
	f.calls = nil
	s, err = c.ReadSnapshot(ctx, true)
	if err != nil || len(s.Issues(true)) != 1 || s.ObservationMode() != "full" || len(f.calls) != 2 || strings.Contains(f.calls[0], "since=") {
		t.Fatal("full reconciliation missing", err, f.calls)
	}
	a, _ = s.Activity("gh-1")
	if len(a) != 0 {
		t.Fatal("deleted comment survived full scan")
	}
}
func TestIncrementalFailuresNeverAdvanceCacheOrCursor(t *testing.T) {
	cause := errors.New("original partial rate request ID")
	limit := &RateLimitError{Cause: cause, RetryAt: time.Now().Add(time.Hour), WaitSource: "retry-after"}
	for _, name := range []string{"rate", "malformed issue JSON", "null comment page", "conflicting duplicate", "bad activity metadata"} {
		t.Run(name, func(t *testing.T) {
			f := newIncrementalFixture(t)
			c := f.client
			ctx := context.Background()
			if _, err := c.ReadSnapshot(ctx, true); err != nil {
				t.Fatal(err)
			}
			ageIncrementalCache(t, c, 31*time.Second, time.Minute)
			before, err := os.ReadFile(c.cache.path(true))
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			switch name {
			case "rate":
				f.deltaCommentErr = limit
			case "malformed issue JSON":
				f.deltaIssueRaw = []byte(`{"not":"pages"}`)
			case "null comment page":
				f.deltaComments = [][]apiComment{nil}
			case "conflicting duplicate":
				f.deltaIssues = [][]apiIssue{{{Number: 1, State: "open", Title: "one", UpdatedAt: now}}, {{Number: 1, State: "open", Title: "two", UpdatedAt: now}}}
			case "bad activity metadata":
				f.deltaComments = [][]apiComment{{{ID: 2, IssueURL: "https://api.github.com/repos/owner/repo/issues/1", Body: activityPrefix + "malformed", UpdatedAt: now}}}
			}
			s, err := c.ReadSnapshot(ctx, true)
			if s != nil || err == nil {
				t.Fatalf("partial result accepted: %+v %v", s, err)
			}
			if name == "rate" {
				var got *RateLimitError
				if !errors.As(err, &got) || !errors.Is(err, cause) || got.RetryAt != limit.RetryAt {
					t.Fatal("original rate error lost", err)
				}
			}
			after, readErr := os.ReadFile(c.cache.path(true))
			if readErr != nil || string(before) != string(after) {
				t.Fatal("failed delta published cursor", readErr)
			}
		})
	}
}
func TestIncrementalUnknownOwnerFallsBackToFullAndKnownPRCommentsStayExcluded(t *testing.T) {
	f := newIncrementalFixture(t)
	c := f.client
	ctx := context.Background()
	f.fullIssues[0] = append(f.fullIssues[0], apiIssue{Number: 9, PullRequest: json.RawMessage(`{}`)})
	if _, err := c.ReadSnapshot(ctx, true); err != nil {
		t.Fatal(err)
	}
	ageIncrementalCache(t, c, 31*time.Second, time.Minute)
	f.deltaComments = [][]apiComment{{{ID: 9, IssueURL: "https://api.github.com/repos/owner/repo/issues/9", Body: activityPrefix + "invalid PR metadata", UpdatedAt: time.Now()}}}
	f.calls = nil
	if s, err := c.ReadSnapshot(ctx, true); err != nil || len(s.Issues(true)) != 2 || len(f.calls) != 2 {
		t.Fatal("known PR classified as task", err, f.calls)
	}
	ageIncrementalCache(t, c, 31*time.Second, time.Minute)
	f.deltaComments = [][]apiComment{{{ID: 8, IssueURL: "https://api.github.com/repos/owner/repo/issues/8", Body: "previously unseen PR", UpdatedAt: time.Now()}}}
	f.fullIssues[0] = append(f.fullIssues[0], apiIssue{Number: 8, PullRequest: json.RawMessage(`{}`)})
	f.calls = nil
	s, err := c.ReadSnapshot(ctx, true)
	if err != nil || s.ObservationMode() != "full" || len(f.calls) != 4 || len(s.Issues(true)) != 2 {
		t.Fatal("unknown owner did not use bounded full scan", err, f.calls)
	}
}

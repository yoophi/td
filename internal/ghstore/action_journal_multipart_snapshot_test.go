package ghstore

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestMultipartSnapshotUsesExistingBulkPages(t *testing.T) {
	a := multipartJournalFixture(t)
	_, root, parts := multipartNativeFixture(t, a)
	issues := [][]apiIssue{{{Number: 1, State: "open"}, {Number: 2, State: "open"}}}
	native := apiComment{ID: 5000, IssueURL: "https://api.github.com/repos/owner/repo/issues/2", Body: "ordinary comment"}
	// Manifest precedes its parts in pagination. Native entries remain intact
	// for cache, change tokens and subsequent conflict checks.
	c, calls := snapshotFixture(issues, [][]apiComment{{root, native}, parts}, nil)
	s, err := c.ReadSnapshot(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	history, err := s.ActionHistory("")
	if err != nil || len(history) != 1 || !reflect.DeepEqual(history[0].ActionJournal, a) {
		t.Fatal("bulk journal resolution", err)
	}
	activity, err := s.Activity("gh-1")
	if err != nil || len(activity) != 1 || activity[0].Kind != "action_journal" || activity[0].OperationID != a.OperationID {
		t.Fatal("transport leaked into user activity", activity, err)
	}
	activity, err = s.Activity("gh-2")
	if err != nil || len(activity) != 1 || activity[0].Message != native.Body {
		t.Fatal("native comment lost", err)
	}
	if len(s.entries[0].Comments) != len(parts)+1 || len(*calls) != 2 {
		t.Fatal("parts were discarded or per-part reads occurred", *calls)
	}
	if _, err := s.ChangeToken(); err != nil {
		t.Fatal(err)
	}
	// A collection containing only an interrupted upload has no action result.
	c, _ = snapshotFixture(issues, [][]apiComment{{parts[0]}}, nil)
	s, err = c.ReadSnapshot(context.Background(), true)
	if err != nil {
		t.Fatal("unpublished parts blocked reading", err)
	}
	history, err = s.ActionHistory("")
	activity, activityErr := s.Activity("gh-1")
	if err != nil || activityErr != nil || len(history) != 0 || len(activity) != 0 {
		t.Fatal("partial upload was treated as an action", err, activityErr)
	}
}

func TestMultipartSnapshotNeverReturnsPartialHistory(t *testing.T) {
	_, root, parts := multipartNativeFixture(t, multipartJournalFixture(t))
	for _, name := range []string{"missing", "edited", "duplicate"} {
		t.Run(name, func(t *testing.T) {
			p := append([]apiComment(nil), parts...)
			switch name {
			case "missing":
				p = p[1:]
			case "edited":
				p[0].UpdatedAt = p[0].UpdatedAt.Add(time.Second)
			case "duplicate":
				copy := p[0]
				copy.ID = 4000
				p = append(p, copy)
			}
			c, calls := snapshotFixture([][]apiIssue{{{Number: 1, State: "open"}}}, [][]apiComment{{root}, p}, nil)
			s, err := c.ReadSnapshot(context.Background(), true)
			if err == nil || s != nil || len(*calls) != 2 {
				t.Fatal("corrupt transport returned a successful partial snapshot", err, *calls)
			}
		})
	}
}

func TestMultipartSnapshotIncrementalUsesUnchangedPartsAndPreservesFailedCursor(t *testing.T) {
	a := multipartJournalFixture(t)
	_, root, parts := multipartNativeFixture(t, a)
	f := newIncrementalFixture(t)
	f.fullComments = [][]apiComment{parts} // Interrupted upload observed first.
	if _, err := f.client.ReadSnapshot(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	ageIncrementalCache(t, f.client, 31*time.Second, time.Minute)
	root.UpdatedAt = time.Now().UTC()
	f.deltaComments = [][]apiComment{{root}} // Parts are outside the delta.
	f.calls = nil
	s, err := f.client.ReadSnapshot(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	history, err := s.ActionHistory("")
	if err != nil || len(history) != 1 || !reflect.DeepEqual(history[0].ActionJournal, a) || len(f.calls) != 2 || s.ObservationMode() != "incremental" {
		t.Fatal("manifest did not resolve from merged baseline", err, f.calls)
	}
	ageIncrementalCache(t, f.client, 31*time.Second, time.Minute)
	before, err := os.ReadFile(f.client.cache.path(true))
	if err != nil {
		t.Fatal(err)
	}
	edited := parts[0]
	edited.UpdatedAt = root.UpdatedAt.Add(time.Second)
	f.deltaComments = [][]apiComment{{edited}}
	s, err = f.client.ReadSnapshot(context.Background(), true)
	if err == nil || s != nil {
		t.Fatal("changed part was reused from baseline", err)
	}
	after, err := os.ReadFile(f.client.cache.path(true))
	if err != nil || string(after) != string(before) {
		t.Fatal("failed multipart delta advanced cache/cursor", err)
	}
	// A forced collection must notice physical deletion too. Incremental feeds
	// retain the existing documented full-reconciliation deletion limitation.
	f.fullComments = [][]apiComment{{root}, parts[1:]}
	s, err = f.client.ReadSnapshot(WithFreshSnapshot(context.Background()), true)
	if err == nil || s != nil {
		t.Fatal("missing native part accepted on full refresh", err)
	}
}

func TestMultipartIssueActivityUsesOneCollection(t *testing.T) {
	_, root, parts := multipartNativeFixture(t, multipartJournalFixture(t))
	calls := 0
	c := &Client{repo: "owner/repo", run: func(_ context.Context, _ string, _ []byte, args ...string) ([]byte, error) {
		calls++
		if args[5] != "repos/owner/repo/issues/1/comments?per_page=100" {
			return nil, fmt.Errorf("unexpected request %s", args[5])
		}
		return json.Marshal([][]apiComment{{root}, parts})
	}}
	issue := &Record{}
	issue.ID, issue.Number = "gh-1", 1
	activities, err := c.readIssueActivity(context.Background(), issue)
	if err != nil || len(activities) != 1 || activities[0].Kind != "action_journal" || calls != 1 {
		t.Fatal("individual issue history lost manifest or repeated reads", err, calls)
	}
}

package issuestore

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/marcus/td/internal/db"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
)

type searchActivityFixture struct {
	rows  map[string][]models.Activity
	calls map[string]int
	err   error
}

func (f *searchActivityFixture) ListActivity(_ context.Context, id string) ([]models.Activity, error) {
	f.calls[id]++
	return f.rows[id], f.err
}
func (*searchActivityFixture) AppendActivity(context.Context, string, models.Activity) (*models.Activity, error) {
	panic("search wrote")
}

func TestGitHubRankedSearchSQLiteParityHistoryWildcardsAndLimit(t *testing.T) {
	database, err := db.Initialize(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	activity := &searchActivityFixture{rows: map[string][]models.Activity{}, calls: map[string]int{}}
	records := []ghstore.Record{}
	for n, issue := range []models.Issue{{Title: "description fixture", Description: "needle in body", Priority: models.PriorityP0}, {Title: "needle", Priority: models.PriorityP4}, {Title: "logging fixture"}, {Title: "historical handoff fixture"}, {Title: "native comment fixture"}} {
		if err := database.CreateIssue(&issue); err != nil {
			t.Fatal(err)
		}
		localID := issue.ID
		issue.ID = fmt.Sprintf("gh-%d", n+1)
		records = append(records, ghstore.Record{Issue: issue})
		switch n {
		case 2:
			if err := database.AddLog(&models.Log{IssueID: localID, Message: "needle from log", Type: models.LogTypeProgress}); err != nil {
				t.Fatal(err)
			}
			activity.rows[issue.ID] = []models.Activity{{Kind: "log", Message: "needle from log"}}
		case 3:
			if err := database.AddHandoff(&models.Handoff{IssueID: localID, Done: []string{"needle old handoff"}}); err != nil {
				t.Fatal(err)
			}
			if err := database.AddHandoff(&models.Handoff{IssueID: localID, Done: []string{"new unrelated handoff"}}); err != nil {
				t.Fatal(err)
			}
			activity.rows[issue.ID] = []models.Activity{{Kind: "handoff", Done: []string{"needle old handoff"}}, {Kind: "handoff", Done: []string{"new unrelated handoff"}}}
		case 4:
			activity.rows[issue.ID] = []models.Activity{{Kind: "comment", Native: true, Message: "needle native comment"}}
		}
	}
	snapshot, err := NewGitHubQuerySnapshot(context.Background(), records, activity)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"needle", "NEEDLE", "ne_dle", "need%", "missing", "%"} {
		a, err := database.SearchIssuesRanked(text, db.ListIssuesOptions{})
		if err != nil {
			t.Fatal(err)
		}
		b, err := snapshot.SearchIssuesRanked(text, db.ListIssuesOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if len(a) != len(b) {
			t.Fatal(text, a, b)
		}
		byTitle := map[string]db.SearchResult{}
		for _, r := range a {
			byTitle[r.Issue.Title] = r
		}
		for _, r := range b {
			other, ok := byTitle[r.Issue.Title]
			if !ok || r.Score != other.Score || r.MatchField != other.MatchField {
				t.Fatal(text, r, other)
			}
		}
	}
	for _, backend := range []interface {
		SearchIssuesRanked(string, db.ListIssuesOptions) ([]db.SearchResult, error)
	}{database, snapshot} {
		rows, err := backend.SearchIssuesRanked("needle", db.ListIssuesOptions{Limit: 1})
		if err != nil || len(rows) != 1 || rows[0].Issue.Title != "needle" || rows[0].Score != 80 {
			t.Fatal("limit before ranking", rows, err)
		}
	}
	for id, calls := range activity.calls {
		if calls != 1 {
			t.Fatal("activity fetched repeatedly", id, calls)
		}
	}
	activity.err = errors.New("permission denied")
	fresh, err := NewGitHubQuerySnapshot(context.Background(), records, activity)
	if err != nil {
		t.Fatal(err)
	}
	if rows, err := fresh.SearchIssuesRanked("missing", db.ListIssuesOptions{}); err == nil || rows != nil {
		t.Fatal("activity error yielded partial results", rows, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fresh, err = NewGitHubQuerySnapshot(ctx, records, activity)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.SearchIssuesRanked("needle", db.ListIssuesOptions{}); err == nil {
		t.Fatal("cancelled search succeeded")
	}
}

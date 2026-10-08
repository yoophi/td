package issuestore

import (
	"context"
	"testing"
	"time"

	"github.com/marcus/td/internal/db"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/query"
)

func TestGitHubQuerySnapshotRelationsReworkAndLimits(t *testing.T) {
	now := time.Now()
	records := []ghstore.Record{
		{Issue: models.Issue{ID: "gh-1", Status: models.StatusOpen, Points: 1}},
		{Issue: models.Issue{ID: "gh-2", Status: models.StatusInProgress, ParentID: "gh-1", Points: 3}, Details: &ghstore.IssueDetails{Dependencies: []string{"gh-1"}, Transitions: []ghstore.TransitionRecord{{Action: "reject", At: now}}}},
		{Issue: models.Issue{ID: "gh-3", Status: models.StatusOpen, Points: 2}, Details: &ghstore.IssueDetails{Transitions: []ghstore.TransitionRecord{{Action: "reject", At: now}, {Action: "review", At: now.Add(time.Second)}}}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source, err := NewGitHubQuerySnapshot(ctx, records, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, expr := range []string{"rework()", "has_open_deps()", "descendant_of(gh-1)"} {
		got, err := query.Execute(source, expr, "fixture", query.ExecuteOptions{})
		if err != nil || len(got) != 1 || got[0].ID != "gh-2" {
			t.Fatalf("%s: %+v %v", expr, got, err)
		}
	}
	got, err := query.ExecuteDetailed(source, "points > 0", "fixture", query.ExecuteOptions{SortBy: "points", SortDesc: true, MaxResults: 2})
	if err != nil || !got.ScanLimited || len(got.Issues) != 2 || got.Issues[0].ID != "gh-2" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := source.ListIssues(db.ListIssuesOptions{Search: "silently ignored"}); err == nil {
		t.Fatal("unsupported source option ignored")
	}
	if _, err := source.GetComments("gh-1"); err == nil {
		t.Fatal("unavailable activity reader returned empty comments")
	}
	if _, err := NewGitHubQuerySnapshot(ctx, append(records, records[0]), nil); err == nil {
		t.Fatal("duplicate page accepted")
	}
	cancel()
	if _, err := query.Execute(source, "status = open", "fixture", query.ExecuteOptions{}); err == nil {
		t.Fatal("cancelled read succeeded")
	}
}

package issuestore

import (
	"context"
	"encoding/json"
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

func TestGitHubQueryHistoricalLogsAndHandoffUseCachedActivity(t *testing.T) {
	original := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	item := ghstore.ImportItem{SourceNamespace: "project", Issue: models.Issue{ID: "td-source", Title: "Original", Type: models.TypeTask, Priority: models.PriorityP2, Status: models.StatusOpen}, Logs: []models.Log{{ID: "log-old", SessionID: "old-worker", Message: "Original progress", Type: models.LogTypeProgress, Timestamp: original}}, Handoffs: []models.Handoff{{ID: "handoff-old", SessionID: "old-worker", Done: []string{"Original handoff"}, Timestamp: original}}}
	data, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := item.Digest()
	doc := &models.PreservedHistory{Kind: "source", SourceKey: item.SourceKey(), SourceDigest: digest, ContentDigest: digest, PartsPresent: 1, PartsExpected: 1, Complete: true, Data: data}
	activity := &searchActivityFixture{rows: map[string][]models.Activity{"gh-1": {{ID: "ghc-100", IssueID: "gh-1", Kind: "comment", Native: true, PreservedHistory: doc}, {ID: "ghc-101", IssueID: "gh-1", Kind: "log", Message: "Current progress", CreatedAt: original.Add(time.Hour), SessionID: "current-worker", LogType: models.LogTypeProgress}}}, calls: map[string]int{}}
	source, err := NewGitHubQuerySnapshot(context.Background(), []ghstore.Record{{Issue: models.Issue{ID: "gh-1", Status: models.StatusOpen}}}, activity)
	if err != nil {
		t.Fatal(err)
	}
	logs, err := source.GetLogs("1", 0)
	if err != nil || len(logs) != 2 || logs[0].Message != "Current progress" || !logs[1].Historical || logs[1].IssueID != "gh-1" || logs[1].SessionID != "old-worker" {
		t.Fatal(logs, err)
	}
	handoff, err := source.GetLatestHandoff("1")
	if err != nil || handoff == nil || !handoff.Historical || handoff.IssueID != "gh-1" || !handoff.Timestamp.Equal(original) {
		t.Fatal(handoff, err)
	}
	if _, err := source.GetLogs("gh-1", 1); err != nil || activity.calls["gh-1"] != 1 {
		t.Fatal("historical view refetched activity", err, activity.calls)
	}
	activity.rows["gh-1"] = append(activity.rows["gh-1"], models.Activity{ID: "ghc-102", IssueID: "gh-1", Kind: "handoff", SessionID: "current-worker", Done: []string{"Current handoff"}, CreatedAt: original.Add(-time.Hour)})
	fresh, err := NewGitHubQuerySnapshot(context.Background(), []ghstore.Record{{Issue: models.Issue{ID: "gh-1", Status: models.StatusOpen}}}, activity)
	if err != nil {
		t.Fatal(err)
	}
	latest, err := fresh.GetLatestHandoff("gh-1")
	if err != nil || latest.Historical || latest.SessionID != "current-worker" {
		t.Fatal("historical future timestamp shadowed current handoff", latest, err)
	}
}

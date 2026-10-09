package serve

import (
	"context"
	"encoding/json"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/reviewpolicy"
	"testing"
	"time"
)

func TestGitHubContextImportedHistorySeparateFromCurrentActivity(t *testing.T) {
	now := time.Now()
	item := ghstore.ImportItem{SourceNamespace: "project", Issue: models.Issue{ID: "td-old", Title: "Original", Type: models.TypeTask, Status: models.StatusOpen, Priority: models.PriorityP2}, Logs: []models.Log{{ID: "old-log", SessionID: "worker", WorkSessionID: "ws-current", Message: "Archived progress", Type: models.LogTypeProgress, Timestamp: now}}, Handoffs: []models.Handoff{{ID: "old-handoff", SessionID: "worker", Done: []string{"Archived handoff"}, Timestamp: now}}}
	data, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := item.Digest()
	if err != nil {
		t.Fatal(err)
	}
	f := monitorFixture(now)
	f.activities = map[string][]models.Activity{}
	f.activities["gh-1"] = []models.Activity{{ID: "ghc-100", IssueID: "gh-1", Kind: "comment", Native: true, CreatedAt: now, PreservedHistory: &models.PreservedHistory{Kind: "source", SourceKey: item.SourceKey(), SourceDigest: digest, ContentDigest: digest, Complete: true, PartsPresent: 1, PartsExpected: 1, Data: data}}}
	s, err := ReadGitHubContext(context.Background(), f, "worker", reviewpolicy.ModeTrusted, nil)
	if err != nil {
		t.Fatal(err)
	}
	history := s.ImportedHistory["gh-1"]
	if len(history.Logs) != 1 || !history.Logs[0].Historical || len(history.Handoffs) != 1 || !history.Handoffs[0].Historical || len(s.Data.ActiveSessions) != 0 || len(s.Data.RecentHandoffs) != 0 {
		t.Fatal("archive lost or became current", s)
	}
	if len(s.Activities["gh-1"]) != 1 || s.Activities["gh-1"][0].Kind != "comment" || s.Activities["gh-1"][0].SessionID != "" {
		t.Fatal("native authority events changed", s.Activities)
	}
	for _, calls := range f.calls {
		if calls != 1 {
			t.Fatal("history projection refetched remote data", f.calls)
		}
	}
}

func TestGitHubContextRetainsFullHistoryAndSingleObservation(t *testing.T) {
	f := monitorFixture(time.Now())
	for n := 0; n < 60; n++ {
		f.activities["gh-1"] = append(f.activities["gh-1"], models.Activity{ID: string(rune('a' + n)), Kind: "log", SessionID: "worker", Message: "history", CreatedAt: time.Now()})
	}
	s, err := ReadGitHubContext(context.Background(), f, "worker", reviewpolicy.ModeTrusted, nil)
	if err != nil || len(s.Data.Activity) != 50 || len(s.Activities["gh-1"]) != 61 || len(s.Records) != len(f.records) {
		t.Fatalf("%+v %v", s, err)
	}
	for _, calls := range f.calls {
		if calls != 1 {
			t.Fatal("history fetched twice", f.calls)
		}
	}
	f.failure = "activity"
	if s, err := ReadGitHubContext(context.Background(), f, "worker", reviewpolicy.ModeTrusted, nil); err == nil || s != nil {
		t.Fatal("partial context returned", s, err)
	}
}

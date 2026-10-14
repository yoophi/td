package monitor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/internal/models"
)

type backendHandoffFixture struct {
	rows   []backendstore.Issue
	events map[string][]models.Activity
}

func (f backendHandoffFixture) Monitor(context.Context, string, bool, string) (backendstore.Monitor, error) {
	return backendstore.Monitor{}, nil
}
func (f backendHandoffFixture) ListIssuesIncludingDeleted(context.Context) ([]backendstore.Issue, error) {
	return f.rows, nil
}
func (f backendHandoffFixture) IssueActivityIncludingDeleted(_ context.Context, id string) ([]models.Activity, error) {
	return f.events[id], nil
}

func TestBackendMonitorHandoffsIncludeArchivedIssuesAndRejectStaleContext(t *testing.T) {
	oldID, newID := "td-aaaaaaaaaaaaaaaa", "td-bbbbbbbbbbbbbbbb"
	now := time.Now().UTC()
	deleted := now
	fixture := backendHandoffFixture{rows: []backendstore.Issue{{Issue: models.Issue{ID: oldID, DeletedAt: &deleted}}, {Issue: models.Issue{ID: newID}}}, events: map[string][]models.Activity{
		oldID: {{ID: "act-old", IssueID: oldID, Kind: "handoff", SessionID: "past", CreatedAt: now.Add(-time.Hour), Done: []string{"archived work"}}},
		newID: {{ID: "act-new", IssueID: newID, Kind: "handoff", SessionID: "current", CreatedAt: now, Remaining: []string{"next"}}, {ID: "log-new", IssueID: newID, Kind: "log", CreatedAt: now}},
	}}
	guardCalls := 0
	source := NewBackendDataSource(context.Background(), fixture, func(context.Context) error { guardCalls++; return nil })
	got := source.Handoffs()
	if got.Error != nil || len(got.Data) != 2 || got.Data[0].ID != "act-new" || got.Data[1].ID != "act-old" || got.Data[1].Done[0] != "archived work" || guardCalls != 2 {
		t.Fatalf("backend handoff history incomplete: %+v, guard=%d", got, guardCalls)
	}
	stale := errors.New("project context changed")
	source = NewBackendDataSource(context.Background(), fixture, func(context.Context) error {
		guardCalls++
		if guardCalls > 3 {
			return stale
		}
		return nil
	})
	if got := source.Handoffs(); !errors.Is(got.Error, stale) || got.Data != nil {
		t.Fatalf("stale handoff history shown: %+v", got)
	}
}

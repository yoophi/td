package monitor

import (
	"context"
	"testing"

	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/internal/models"
)

type backendStatsFixture struct{ value backendstore.Stats }

func (f backendStatsFixture) Monitor(context.Context, string, bool, string) (backendstore.Monitor, error) {
	return backendstore.Monitor{}, nil
}
func (f backendStatsFixture) Stats(context.Context) (backendstore.Stats, error) {
	return f.value, nil
}

func TestBackendMonitorStatsConversionAndContextGuard(t *testing.T) {
	fixture := backendStatsFixture{value: backendstore.Stats{Total: 3, ByStatus: map[string]int{"open": 2, "closed": 1}, ByType: map[string]int{"task": 3}, ByPriority: map[string]int{"P2": 3}, TotalPoints: 8, OldestOpen: &backendstore.Issue{Issue: models.Issue{ID: "td-0123456789abcdef"}, Revision: "r-1"}}}
	source := NewBackendDataSource(context.Background(), fixture, func(context.Context) error { return nil })
	msg := source.Stats()
	if msg.Error != nil || msg.Data == nil || msg.Data.ExtendedStats == nil || msg.Data.ExtendedStats.ByStatus[models.StatusOpen] != 2 || msg.Data.ExtendedStats.TotalPoints != 8 || msg.Data.ExtendedStats.OldestOpen.ID != fixture.value.OldestOpen.ID {
		t.Fatalf("backend statistics omitted: %+v", msg)
	}
	fixture.value.ByStatus = map[string]int{"unknown": 1}
	source = NewBackendDataSource(context.Background(), fixture, func(context.Context) error { return nil })
	if msg = source.Stats(); msg.Error == nil || msg.Data.ExtendedStats != nil {
		t.Fatalf("invalid statistic accepted: %+v", msg)
	}
}

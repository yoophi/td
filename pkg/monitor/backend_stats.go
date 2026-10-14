package monitor

import (
	"context"
	"fmt"

	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/internal/models"
)

type backendStatsReader interface {
	Stats(context.Context) (backendstore.Stats, error)
}

func (s *BackendDataSource) Stats() StatsDataMsg {
	fail := func(err error) StatsDataMsg { return StatsDataMsg{Data: &StatsData{Error: err}, Error: err} }
	if err := s.ctx.Err(); err != nil {
		return fail(err)
	}
	if s.guard == nil {
		return fail(fmt.Errorf("backend monitor context unavailable"))
	}
	if err := s.guard(s.ctx); err != nil {
		return fail(err)
	}
	reader, ok := s.reader.(backendStatsReader)
	if !ok {
		return fail(fmt.Errorf("backend statistics reader unavailable"))
	}
	observed, err := reader.Stats(s.ctx)
	if err != nil {
		return fail(err)
	}
	if err := s.guard(s.ctx); err != nil {
		return fail(err)
	}
	stats := &models.ExtendedStats{Total: observed.Total, ByStatus: map[models.Status]int{}, ByType: map[models.Type]int{}, ByPriority: map[models.Priority]int{}, CreatedToday: observed.CreatedToday, CreatedThisWeek: observed.CreatedThisWeek, TotalPoints: observed.TotalPoints, AvgPointsPerTask: observed.AvgPointsPerTask, CompletionRate: observed.CompletionRate, TotalLogs: observed.TotalLogs, TotalHandoffs: observed.TotalHandoffs, MostActiveSession: observed.MostActiveSession}
	for key, count := range observed.ByStatus {
		if !models.IsValidStatus(models.Status(key)) || count < 0 {
			return fail(fmt.Errorf("invalid backend status statistic"))
		}
		stats.ByStatus[models.Status(key)] = count
	}
	for key, count := range observed.ByType {
		if !models.IsValidType(models.Type(key)) || count < 0 {
			return fail(fmt.Errorf("invalid backend type statistic"))
		}
		stats.ByType[models.Type(key)] = count
	}
	for key, count := range observed.ByPriority {
		if !models.IsValidPriority(models.Priority(key)) || count < 0 {
			return fail(fmt.Errorf("invalid backend priority statistic"))
		}
		stats.ByPriority[models.Priority(key)] = count
	}
	if observed.OldestOpen != nil {
		value := observed.OldestOpen.Issue
		stats.OldestOpen = &value
	}
	if observed.NewestTask != nil {
		value := observed.NewestTask.Issue
		stats.NewestTask = &value
	}
	if observed.LastClosed != nil {
		value := observed.LastClosed.Issue
		stats.LastClosed = &value
	}
	return StatsDataMsg{Data: &StatsData{ExtendedStats: stats}}
}

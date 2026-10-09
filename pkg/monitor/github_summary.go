package monitor

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
)

type MonitorStatsSource interface{ Stats() StatsDataMsg }
type MonitorHandoffsSource interface{ Handoffs() HandoffsDataMsg }
type githubStatsReader interface {
	ExtendedStats(context.Context, time.Time) (*models.ExtendedStats, error)
}

func (s *GitHubDataSource) Stats() StatsDataMsg {
	fail := func(err error) StatsDataMsg { return StatsDataMsg{Data: &StatsData{Error: err}, Error: err} }
	if err := s.ctx.Err(); err != nil {
		return fail(err)
	}
	raw, err := s.open(s.ctx)
	if err != nil {
		return fail(err)
	}
	c, ok := raw.(githubStatsReader)
	if !ok {
		return fail(fmt.Errorf("GitHub monitor statistics reader unavailable"))
	}
	stats, err := c.ExtendedStats(s.ctx, time.Now())
	if err != nil {
		return fail(err)
	}
	if stats == nil {
		return fail(fmt.Errorf("GitHub monitor statistics observation missing"))
	}
	if err := s.ctx.Err(); err != nil {
		return fail(err)
	}
	return StatsDataMsg{Data: &StatsData{ExtendedStats: stats}}
}

// Handoffs retains shared history on deleted tasks, matching SQLite history.
// A full listing and separate activity reads are not an atomic snapshot.
func (s *GitHubDataSource) Handoffs() HandoffsDataMsg {
	fail := func(err error) HandoffsDataMsg { return HandoffsDataMsg{Error: err} }
	if err := s.ctx.Err(); err != nil {
		return fail(err)
	}
	c, err := s.open(s.ctx)
	if err != nil {
		return fail(err)
	}
	var observed *ghstore.Snapshot
	var records []ghstore.Record
	if bulk, ok := c.(ghstore.SnapshotReader); ok {
		observed, err = bulk.ReadSnapshot(s.ctx, true)
		if err == nil && observed == nil {
			err = fmt.Errorf("GitHub snapshot observation missing")
		}
		if err == nil {
			records = observed.Issues(true)
		}
	} else {
		records, err = c.ListIncludingDeleted(s.ctx, true)
	}
	if err != nil {
		return fail(err)
	}
	out := []models.Handoff{}
	tasks := map[string]bool{}
	activities := map[string]bool{}
	for _, r := range records {
		if err := s.ctx.Err(); err != nil {
			return fail(err)
		}
		if tasks[r.ID] {
			return fail(fmt.Errorf("handoff listing repeated %s", r.ID))
		}
		tasks[r.ID] = true
		var events []models.Activity
		if observed != nil {
			events, err = observed.Activity(r.ID)
		} else {
			events, err = c.ListActivityIncludingDeleted(s.ctx, r.ID)
		}
		if err != nil {
			return fail(fmt.Errorf("handoffs on %s: %w", r.ID, err))
		}
		for _, a := range events {
			if a.ID == "" || activities[a.ID] || a.IssueID != r.ID {
				return fail(fmt.Errorf("invalid or repeated handoff activity observation on %s", r.ID))
			}
			activities[a.ID] = true
			switch a.Kind {
			case "handoff":
				out = append(out, models.Handoff{ID: a.ID, IssueID: r.ID, SessionID: a.SessionID, Timestamp: a.CreatedAt, Done: slices.Clone(a.Done), Remaining: slices.Clone(a.Remaining), Decisions: slices.Clone(a.Decisions), Uncertain: slices.Clone(a.Uncertain)})
			case "log", "comment":
			default:
				return fail(fmt.Errorf("unsupported handoff activity kind %q", a.Kind))
			}
		}
	}
	slices.SortFunc(out, func(a, b models.Handoff) int {
		if cmp := b.Timestamp.Compare(a.Timestamp); cmp != 0 {
			return cmp
		}
		return strings.Compare(a.ID, b.ID)
	})
	if len(out) > 50 {
		out = out[:50]
	}
	if err := s.ctx.Err(); err != nil {
		return fail(err)
	}
	return HandoffsDataMsg{Data: out}
}

package monitor

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
)

type summaryFixture struct {
	monitorDataFixture
	stats      *models.ExtendedStats
	statsErr   error
	statsReads int
}

func (f *summaryFixture) ExtendedStats(ctx context.Context, _ time.Time) (*models.ExtendedStats, error) {
	f.statsReads++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return f.stats, f.statsErr
}
func summarySource(f *summaryFixture) *GitHubDataSource {
	return &GitHubDataSource{ctx: context.Background(), open: func(context.Context) (GitHubMonitorReader, error) { return f, nil }}
}
func TestGitHubMonitorSummaryStatsAndProviderErrorsWithoutSQLite(t *testing.T) {
	f := &summaryFixture{stats: &models.ExtendedStats{Total: 7, TotalHandoffs: 9}}
	s := summarySource(f)
	msg := Model{DataSource: s}.fetchStats()().(StatsDataMsg)
	if msg.Error != nil || msg.Data.ExtendedStats.Total != 7 || f.statsReads != 1 {
		t.Fatalf("%+v", msg)
	}
	failure := errors.New("rate limit")
	f.statsErr = failure
	msg = s.Stats()
	if !errors.Is(msg.Error, failure) || msg.Data.ExtendedStats != nil || !errors.Is(msg.Data.Error, failure) {
		t.Fatal("statistics failure lost")
	}
	f.statsErr = nil
	f.stats = nil
	if msg = s.Stats(); msg.Error == nil {
		t.Fatal("nil statistics accepted")
	}
	for _, cmd := range []func() any{func() any { return Model{DataSource: dashboardOnlyFixture{}}.fetchStats()() }, func() any { return Model{DataSource: dashboardOnlyFixture{}}.fetchHandoffs()() }} {
		switch msg := cmd().(type) {
		case StatsDataMsg:
			if msg.Error == nil {
				t.Fatal("stats fell back to SQLite")
			}
		case HandoffsDataMsg:
			if msg.Error == nil {
				t.Fatal("handoffs fell back to SQLite")
			}
		default:
			t.Fatalf("%T", msg)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.ctx = ctx
	before := f.statsReads
	if msg = s.Stats(); !errors.Is(msg.Error, context.Canceled) || f.statsReads != before {
		t.Fatal("cancelled stats read")
	}
	if msg := s.Handoffs(); !errors.Is(msg.Error, context.Canceled) {
		t.Fatal("cancelled handoff read")
	}
}
func TestGitHubMonitorHandoffsSortLimitAndDeletedHistory(t *testing.T) {
	now := time.Now().UTC()
	f := &summaryFixture{monitorDataFixture: monitorDataFixture{boardSourceFixture: boardSourceFixture{records: []ghstore.Record{{Issue: models.Issue{ID: "gh-1"}}, {Issue: models.Issue{ID: "gh-2", DeletedAt: &now}}}}, activity: map[string][]models.Activity{}}}
	for i := 0; i < 60; i++ {
		id := "gh-1"
		if i%2 == 0 {
			id = "gh-2"
		}
		f.activity[id] = append(f.activity[id], models.Activity{ID: fmt.Sprintf("handoff-%02d", i), Kind: "handoff", IssueID: id, CreatedAt: now.Add(time.Duration(i/2) * time.Second), Done: []string{fmt.Sprint(i)}, Remaining: []string{"next"}, Decisions: []string{"decision"}, Uncertain: []string{"question"}})
	}
	s := summarySource(f)
	msg := Model{DataSource: s}.fetchHandoffs()().(HandoffsDataMsg)
	if msg.Error != nil || len(msg.Data) != 50 || msg.Data[0].ID != "handoff-58" || msg.Data[1].ID != "handoff-59" || msg.Data[0].IssueID != "gh-2" {
		t.Fatalf("%+v", msg)
	}
	msg.Data[0].Done[0] = "changed"
	if f.activity["gh-2"][29].Done[0] == "changed" {
		t.Fatal("handoff data aliases reader storage")
	}
	f.records = nil
	if msg = s.Handoffs(); msg.Error != nil || msg.Data == nil || len(msg.Data) != 0 {
		t.Fatalf("empty %+v", msg)
	}
}
func TestGitHubMonitorHandoffsRejectsPartialAndInvalidObservations(t *testing.T) {
	for _, kind := range []string{"activity-error", "duplicate-task", "duplicate-event", "wrong-issue", "unknown-kind"} {
		t.Run(kind, func(t *testing.T) {
			f := &summaryFixture{monitorDataFixture: monitorDataFixture{boardSourceFixture: boardSourceFixture{records: []ghstore.Record{{Issue: models.Issue{ID: "gh-1"}}}}, activity: map[string][]models.Activity{"gh-1": {{ID: "event-1", IssueID: "gh-1", Kind: "handoff"}}}}}
			switch kind {
			case "activity-error":
				f.activityErr = errors.New("permission denied")
			case "duplicate-task":
				f.records = append(f.records, f.records[0])
			case "duplicate-event":
				f.activity["gh-1"] = append(f.activity["gh-1"], f.activity["gh-1"][0])
			case "wrong-issue":
				f.activity["gh-1"][0].IssueID = "gh-9"
			case "unknown-kind":
				f.activity["gh-1"][0].Kind = "invalid"
			}
			msg := summarySource(f).Handoffs()
			if msg.Error == nil || msg.Data != nil {
				t.Fatalf("partial accepted %+v", msg)
			}
		})
	}
}

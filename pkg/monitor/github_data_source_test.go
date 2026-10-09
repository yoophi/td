package monitor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
)

type monitorDataFixture struct {
	boardSourceFixture
	activity    map[string][]models.Activity
	activityErr error
	actor       string
}

func (f *monitorDataFixture) ListIncludingDeleted(ctx context.Context, all bool) ([]ghstore.Record, error) {
	return f.List(ctx, all)
}
func (f *monitorDataFixture) ListActivityIncludingDeleted(ctx context.Context, id string) ([]models.Activity, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return f.activity[id], f.activityErr
}
func (f *monitorDataFixture) ObserveMonitorReview(_ context.Context, _ *ghstore.Record, actor string) (*ghstore.MonitorReviewFacts, error) {
	f.actor = actor
	return &ghstore.MonitorReviewFacts{Fresh: true}, nil
}
func TestGitHubMonitorDataSourceFocusActorAndDeletedActivity(t *testing.T) {
	now := time.Now().UTC()
	deleted := now.Add(-time.Minute)
	f := &monitorDataFixture{boardSourceFixture: boardSourceFixture{records: []ghstore.Record{
		{Issue: models.Issue{ID: "gh-1", Title: "Review", Status: models.StatusInReview}},
		{Issue: models.Issue{ID: "gh-2", Title: "Deleted", Status: models.StatusClosed, DeletedAt: &deleted}},
	}}, activity: map[string][]models.Activity{"gh-2": {{ID: "comment-2", Kind: "comment", CreatedAt: now, Message: "retained"}}}}
	focus := "gh-1"
	reads := 0
	source := &GitHubDataSource{ctx: context.Background(), baseDir: t.TempDir(), actor: "actual-monitor", open: func(context.Context) (GitHubMonitorReader, error) { reads++; return f, nil }, focus: func(context.Context) (*string, error) { return &focus, nil }}
	m := Model{DataSource: source}
	msg := m.fetchData()().(RefreshDataMsg)
	if msg.Error != nil || msg.FocusedIssue == nil || msg.FocusedIssue.ID != focus || f.actor != "actual-monitor" || len(msg.Activity) != 1 || len(msg.TaskList.Closed) != 0 {
		t.Fatalf("%+v", msg)
	}
	focus = "gh-2"
	msg = source.Fetch("", true, SortByPriority)
	if msg.Error != nil || msg.FocusedIssue != nil || reads != 2 {
		t.Fatalf("deleted focus %+v", msg)
	}
	f.activityErr = errors.New("permission denied")
	msg = source.Fetch("", false, SortByPriority)
	if msg.Error == nil || msg.HasIssues || len(msg.Activity) != 0 {
		t.Fatal("partial refresh exposed")
	}
}
func TestGitHubMonitorRefreshFailurePreservesDashboardAndCancellation(t *testing.T) {
	old := models.Issue{ID: "gh-7"}
	m := Model{FocusedIssue: &old, InProgress: []models.Issue{old}, TaskList: TaskListData{Ready: []models.Issue{old}}, HasIssues: true}
	result, cmd := m.Update(RefreshDataMsg{Error: errors.New("rate limited")})
	next := result.(Model)
	if cmd != nil || !next.StatusIsError || next.FocusedIssue.ID != "gh-7" || len(next.InProgress) != 1 || len(next.TaskList.Ready) != 1 || !next.HasIssues {
		t.Fatal("failed refresh replaced display")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	source := NewGitHubDataSource(ctx, t.TempDir(), models.GitHubStoreConfig{}, "actual", "actual-branch", func(context.Context) (*string, error) { t.Fatal("cancelled refresh read focus"); return nil, nil })
	if msg := source.Fetch("", false, SortByPriority); !errors.Is(msg.Error, context.Canceled) {
		t.Fatalf("%+v", msg)
	}
}
func TestGitHubMonitorDataSourceRejectsMissingSessionAndFocusFailure(t *testing.T) {
	opens := 0
	s := &GitHubDataSource{ctx: context.Background(), baseDir: t.TempDir(), focus: func(context.Context) (*string, error) { return nil, errors.New("session changed") }, open: func(context.Context) (GitHubMonitorReader, error) { opens++; return &monitorDataFixture{}, nil }}
	if msg := s.Fetch("", false, SortByPriority); msg.Error == nil || opens != 0 {
		t.Fatal("missing actor accepted")
	}
	s.actor = "actual"
	if msg := s.Fetch("", false, SortByPriority); msg.Error == nil || msg.HasIssues {
		t.Fatal("focus failure hidden")
	}
}

func TestGitHubMonitorRateLimitPreservesOriginalAndStopsCallsUntilReset(t *testing.T) {
	original := errors.New("gh: API rate limit exceeded (HTTP 403); request ID D352:CDDF; timestamp 2026-10-09 15:03:27 UTC")
	limit := &ghstore.RateLimitError{Cause: original, RetryAt: time.Now().Add(time.Hour), WaitSource: "x-ratelimit-reset"}
	opens := 0
	s := &GitHubDataSource{ctx: context.Background(), baseDir: t.TempDir(), actor: "actual", focus: func(context.Context) (*string, error) { return nil, nil }, open: func(context.Context) (GitHubMonitorReader, error) {
		opens++
		return nil, limit
	}}
	for i := 0; i < 3; i++ {
		msg := s.Fetch("", false, SortByPriority)
		if !errors.Is(msg.Error, original) || msg.Error.Error() != limit.Error() || msg.HasIssues {
			t.Fatalf("lost diagnostic or exposed partial dashboard: %+v", msg)
		}
	}
	if opens != 1 {
		t.Fatalf("retried before reset: opens=%d", opens)
	}
	s.retryAt = time.Now().Add(-time.Second)
	s.Fetch("", false, SortByPriority)
	if opens != 2 {
		t.Fatal("read did not resume after reset")
	}
}

type waitingMonitorRefresh struct {
	monitorDataFixture
	entered chan struct{}
	release chan struct{}
}

func (f *waitingMonitorRefresh) ListIncludingDeleted(ctx context.Context, _ bool) ([]ghstore.Record, error) {
	close(f.entered)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-f.release:
		return nil, nil
	}
}
func TestGitHubMonitorSlowRefreshDoesNotQueueSweeps(t *testing.T) {
	f := &waitingMonitorRefresh{entered: make(chan struct{}), release: make(chan struct{})}
	source := &GitHubDataSource{ctx: context.Background(), baseDir: t.TempDir(), actor: "actual", focus: func(context.Context) (*string, error) { return nil, nil }, open: func(context.Context) (GitHubMonitorReader, error) { return f, nil }}
	done := make(chan RefreshDataMsg, 1)
	go func() { done <- source.Fetch("old", false, SortByPriority) }()
	<-f.entered
	if !source.IsRefreshing() {
		t.Fatal("running refresh not visible")
	}
	if msg := source.Fetch("new", false, SortByPriority); !msg.Skipped || msg.Error != nil {
		t.Fatal("overlap queued or misreported", msg)
	}
	close(f.release)
	msg := <-done
	if source.IsRefreshing() {
		t.Fatal("finished refresh still busy")
	}
	if msg.Error != nil || msg.remoteFilter == nil || msg.remoteFilter.search != "old" {
		t.Fatal(msg)
	}
	m := Model{SearchQuery: "new", DataSource: source, FocusedIssue: &models.Issue{ID: "gh-9"}}
	result, cmd := m.Update(msg)
	if result.(Model).FocusedIssue.ID != "gh-9" || cmd == nil {
		t.Fatal("stale query replaced display or lost refresh")
	}
}

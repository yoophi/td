package monitor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
)

func TestGitHubRateLimitFooterShowsScheduledLocalTimeAndHeader(t *testing.T) {
	for _, header := range []string{"retry-after", "x-ratelimit-reset"} {
		for _, width := range []int{45, 80, 120} {
			m := NewModel(nil, "actual", time.Minute, "test", t.TempDir())
			m.DataSource = dashboardOnlyFixture{}
			m.Width = width
			until := time.Now().Add(time.Hour).UTC()
			m.LastRefresh = until.Add(-2 * time.Hour)
			m.TaskList.Ready = []models.Issue{{ID: "gh-7"}}
			result, cmd := m.Update(RefreshDataMsg{Error: errors.New("GitHub rate limit original diagnostic"), RetryAt: until, RetrySource: header})
			m = result.(Model)
			if cmd == nil || m.TaskList.Ready[0].ID != "gh-7" {
				t.Fatal("no retry scheduled or previous data lost")
			}
			lines := strings.Split(ansi.Strip(m.renderFooter()), "\n")
			if len(lines) != 3 || !strings.Contains(lines[1], "Error refreshing monitor:") || !strings.Contains(lines[2], formatLocalTime(until, "15:04:05")) || !strings.Contains(lines[2], header) {
				t.Fatal("scheduled time/header missing", lines)
			}
			for _, line := range lines {
				if ansi.StringWidth(line) > width {
					t.Fatal("footer overflow", line)
				}
			}
			generation := m.gitHubRetryGeneration
			result, cmd = m.Update(RefreshDataMsg{Error: errors.New("same cooldown"), RetryAt: until, RetrySource: header})
			m = result.(Model)
			if cmd != nil || m.gitHubRetryGeneration != generation {
				t.Fatal("duplicate wait scheduled another timer")
			}
			result, _ = m.Update(RefreshDataMsg{Timestamp: time.Now()})
			m = result.(Model)
			if !m.GitHubRetryAt.IsZero() || m.StatusIsError || strings.Contains(ansi.Strip(m.renderFooter()), "Next:") {
				t.Fatal("success retained cooldown", m.renderFooter())
			}
			if _, cmd := m.Update(GitHubRetryMsg{Generation: generation}); cmd != nil {
				t.Fatal("stale timer triggered another read")
			}
		}
	}
}

func TestGitHubRetryRefreshDoesNotStartSecondPollChainInsideForm(t *testing.T) {
	m := NewModel(nil, "actual", time.Minute, "test", t.TempDir())
	m.DataSource = dashboardOnlyFixture{}
	opened, _ := m.openNewIssueForm()
	m = opened.(Model)
	m.FormState.Title = "keep draft"
	result, cmd := m.Update(RefreshDataMsg{Error: errors.New("rate limited"), RetryAt: time.Now().Add(time.Hour), RetrySource: "retry-after"})
	m = result.(Model)
	if cmd == nil || m.FormState.Title != "keep draft" {
		t.Fatal("form swallowed retry feedback or draft changed")
	}
	result, cmd = m.Update(GitHubRetryMsg{Generation: m.gitHubRetryGeneration})
	m = result.(Model)
	if cmd == nil {
		t.Fatal("scheduled read missing")
	}
	if _, ok := cmd().(RefreshDataMsg); !ok {
		t.Fatal("retry started a second tick chain")
	}
	if !m.GitHubRetryAt.IsZero() || m.FormState.Title != "keep draft" {
		t.Fatal("retry retained scheduled time or changed draft")
	}
}

func TestGitHubRetryNewDeadlineSupersedesOldTimerAndOrdinaryErrorClearsIt(t *testing.T) {
	m := NewModel(nil, "actual", time.Minute, "test", t.TempDir())
	m.DataSource = dashboardOnlyFixture{}
	first := time.Now().Add(time.Hour)
	result, _ := m.Update(RefreshDataMsg{Error: errors.New("first limit"), RetryAt: first, RetrySource: "retry-after"})
	m = result.(Model)
	old := m.gitHubRetryGeneration
	later := first.Add(time.Hour)
	result, cmd := m.Update(RefreshDataMsg{Error: errors.New("extended limit"), RetryAt: later, RetrySource: "x-ratelimit-reset"})
	m = result.(Model)
	if cmd == nil || !m.GitHubRetryAt.Equal(later) || m.GitHubRetrySource != "x-ratelimit-reset" {
		t.Fatal("new deadline did not replace old schedule")
	}
	if _, cmd := m.Update(GitHubRetryMsg{Generation: old}); cmd != nil {
		t.Fatal("old deadline triggered premature fetch")
	}
	result, cmd = m.Update(RefreshDataMsg{Error: errors.New("ordinary network failure")})
	m = result.(Model)
	if cmd != nil || !m.GitHubRetryAt.IsZero() || !m.StatusIsError {
		t.Fatal("ordinary failure retained retry timer or cleared error")
	}
}

func TestGitHubRateLimitFetchCarriesStableDeadlineAndFallback(t *testing.T) {
	for _, header := range []string{"retry-after", "x-ratelimit-reset", ""} {
		until := time.Now().Add(time.Hour)
		if header == "" {
			until = time.Time{}
		}
		limit := &ghstore.RateLimitError{Cause: errors.New("original rate limit"), RetryAt: until, WaitSource: header}
		calls := 0
		s := &GitHubDataSource{ctx: context.Background(), baseDir: t.TempDir(), actor: "actual", focus: func(context.Context) (*string, error) { return nil, nil }, open: func(context.Context) (GitHubMonitorReader, error) { calls++; return nil, limit }}
		first := s.Fetch("", false, SortByPriority)
		second := s.Fetch("", false, SortByPriority)
		if calls != 1 || first.Error != limit || !first.RetryAt.Equal(second.RetryAt) || !first.RetryAt.After(time.Now()) {
			t.Fatal("cooldown retried or moved deadline", first, second, calls)
		}
		expected := header
		if header == "" {
			expected = "fallback 1m"
		}
		if first.RetrySource != expected {
			t.Fatal("deadline source missing", first.RetrySource)
		}
	}
}

package monitor

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/marcus/td/internal/features"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
)

// MonitorDataSource supplies one complete dashboard refresh. Errors must retain
// the previous display rather than presenting a partially observed dashboard.
type MonitorDataSource interface {
	Fetch(search string, includeClosed bool, sort SortMode) RefreshDataMsg
}

// GitHubDataSource keeps cancellation and actual session identity scoped to its
// owner. Each refresh reopens the configured store and reads device-local focus.
type monitorRefreshFilter struct {
	search        string
	includeClosed bool
	sort          SortMode
}

type GitHubDataSource struct {
	refreshMu      sync.Mutex
	ctx            context.Context
	baseDir, actor string
	branch         string
	open           func(context.Context) (GitHubMonitorReader, error)
	focus          func(context.Context) (*string, error)
}

func NewGitHubDataSource(ctx context.Context, baseDir string, selected models.GitHubStoreConfig, actor, branch string, focus func(context.Context) (*string, error)) *GitHubDataSource {
	return &GitHubDataSource{ctx: ctx, baseDir: baseDir, actor: actor, branch: branch, focus: focus, open: func(ctx context.Context) (GitHubMonitorReader, error) { return ghstore.Open(ctx, baseDir, &selected) }}
}
func (s *GitHubDataSource) Fetch(search string, includeClosed bool, sort SortMode) (result RefreshDataMsg) {
	// A slow GitHub refresh must not queue a new API sweep every poll tick.
	if !s.refreshMu.TryLock() {
		return RefreshDataMsg{Skipped: true}
	}
	defer s.refreshMu.Unlock()
	defer func() { result.remoteFilter = &monitorRefreshFilter{search, includeClosed, sort} }()
	fail := func(err error) RefreshDataMsg { return RefreshDataMsg{Error: err} }
	if err := s.ctx.Err(); err != nil {
		return fail(err)
	}
	if s.actor == "" || s.focus == nil {
		return fail(fmt.Errorf("GitHub monitor requires actual session and focus reader"))
	}
	mode, err := features.ResolveReviewPolicyMode(s.baseDir)
	if err != nil {
		return fail(err)
	}
	client, err := s.open(s.ctx)
	if err != nil {
		return fail(err)
	}
	focus, err := s.focus(s.ctx)
	if err != nil {
		return fail(err)
	}
	msg, err := FetchGitHubData(s.ctx, client, s.actor, mode, focus, search, "auto", includeClosed, sort, time.Now())
	if err != nil {
		return fail(err)
	}
	msg.Transitions = s.transitionHandles(msg.observedIssues)
	return *msg
}

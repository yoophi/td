package monitor

import (
	"context"
	"fmt"
	"time"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/ghcontext"
	"github.com/marcus/td/internal/ghstore"
)

// NewGitHubModel verifies the configured remote before entering the UI. It owns
// request cancellation, uses the actual device-local actor, and never starts
// SQLite or td-sync. Call Close even if the UI fails to launch.
func NewGitHubModel(ctx context.Context, baseDir string, interval time.Duration, ver string) (Model, error) {
	cfg, err := config.Load(baseDir)
	if err != nil {
		return Model{}, err
	}
	store, err := config.Store(cfg)
	if err != nil {
		return Model{}, err
	}
	if store != config.StoreGitHub || cfg.GitHub == nil {
		return Model{}, fmt.Errorf("GitHub monitor requires a configured gh-issue store")
	}
	if _, err := ghstore.Open(ctx, baseDir, cfg.GitHub); err != nil {
		return Model{}, err
	}
	scope, err := ghcontext.Resolve(ctx, baseDir, cfg.GitHub.Repo)
	if err != nil {
		return Model{}, err
	}
	state, err := scope.Update(ctx, nil)
	if err != nil {
		return Model{}, err
	}
	actor := state.Session.ID
	requestCtx, cancel := context.WithCancel(ctx)
	focus := func(ctx context.Context) (*string, error) {
		current, err := scope.Update(ctx, nil)
		if err != nil {
			return nil, err
		}
		if current.Session.ID != actor {
			return nil, fmt.Errorf("monitor session changed; restart the monitor before issuing new requests")
		}
		if current.Focus == "" {
			return nil, nil
		}
		value := current.Focus
		return &value, nil
	}
	m := NewModel(nil, actor, interval, ver, baseDir)
	m.DataSource = NewGitHubDataSource(requestCtx, baseDir, *cfg.GitHub, actor, scope.Branch, focus)
	m.BoardSource = NewGitHubBoardSource(requestCtx, baseDir, *cfg.GitHub, actor)
	m.syncRuntime = newSyncRuntime(nil, SyncOptions{Disabled: true}, func() error { cancel(); return nil })
	return m, nil
}

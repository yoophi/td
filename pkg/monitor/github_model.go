package monitor

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/ghcontext"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/pkg/notes"
)

// NewGitHubModel verifies the configured remote before entering the UI. It owns
// request cancellation, uses the actual device-local actor, and never starts
// SQLite or td-sync. Call Close even if the UI fails to launch.
func NewGitHubModel(ctx context.Context, baseDir string, interval time.Duration, ver string) (Model, error) {
	return NewGitHubModelForWorktree(ctx, baseDir, baseDir, interval, ver)
}

// NewGitHubModelForWorktree separates shared project configuration from the
// current linked worktree's actor and device-local UI preferences.
func NewGitHubModelForWorktree(ctx context.Context, baseDir, worktreeDir string, interval time.Duration, ver string) (Model, error) {
	if baseDir == "" {
		current, err := os.Getwd()
		if err != nil {
			return Model{}, err
		}
		baseDir = current
	}
	if worktreeDir == "" {
		current, err := os.Getwd()
		if err != nil {
			return Model{}, err
		}
		worktreeDir = current
	}
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
	scope, err := ghcontext.Resolve(ctx, worktreeDir, cfg.GitHub.Repo)
	if err != nil {
		return Model{}, err
	}
	state, err := scope.Update(ctx, nil)
	if err != nil {
		return Model{}, err
	}
	actor := state.Session.ID
	preferences := monitorPreferencesForScope(ctx, scope)
	prefs, err := preferences.Load()
	if err != nil {
		return Model{}, err
	}
	requestCtx, cancel := context.WithCancel(ctx)
	preferences.ctx = requestCtx
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
	// Each complete refresh visits task histories; SQLite's 2s default would
	// exhaust GitHub's REST quota. Longer caller intervals remain respected.
	interval = max(interval, 5*time.Minute)
	m := NewModel(nil, actor, interval, ver, baseDir)
	m.notesLifetimeCancel()
	m.notesLifetimeCancel = cancel
	m.NotesContext = requestCtx
	m.NotesFactory = func(ctx context.Context) (MonitorNoteStore, error) {
		current, err := scope.Update(ctx, nil)
		if err != nil {
			return nil, err
		}
		if current.Session.ID != actor {
			return nil, fmt.Errorf("monitor session changed; restart the monitor before notes requests")
		}
		return notes.OpenGitHubWithContext(ctx, worktreeDir, cfg.GitHub.Remote, cfg.GitHub.Repo)
	}
	m.preferences = preferences
	m.PaneHeights = prefs.PaneHeights
	m.DataSource = NewGitHubDataSource(requestCtx, baseDir, *cfg.GitHub, actor, scope.Branch, focus)
	boards := NewGitHubBoardSource(requestCtx, baseDir, *cfg.GitHub, actor)
	boards.preferences = preferences
	m.BoardSource = boards
	m.syncRuntime = newSyncRuntime(nil, SyncOptions{Disabled: true}, func() error { cancel(); return nil })
	return m, nil
}

// PendingRemoteWrite reports a request whose outcome is not yet confirmed.
// Cancelling transport cannot undo writes already accepted by GitHub.
func (m Model) PendingRemoteWrite() bool {
	return m.DataSource != nil && ((m.NotesPending && m.NotesWriting) || (m.WorkflowPending && m.WorkflowWriting) || m.DeletePending || m.BoardMovePending || m.BoardEditorPending)
}

package monitor

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"time"

	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/internal/config"
)

// NewBackendModelForWorktree attaches a terminal or embedded monitor to the
// configured backend project without opening the local SQLite task database.
func NewBackendModelForWorktree(ctx context.Context, baseDir, worktreeDir string, interval time.Duration, version string) (Model, error) {
	cfg, err := config.Load(baseDir)
	if err != nil {
		return Model{}, err
	}
	store, err := config.Store(cfg)
	if err != nil {
		return Model{}, err
	}
	if store != config.StoreBackend || cfg.Backend == nil {
		return Model{}, fmt.Errorf("backend monitor requires a configured td-backend store")
	}
	connection, err := backendstore.Open(ctx, baseDir, worktreeDir)
	if err != nil {
		return Model{}, err
	}
	if err := connection.Client.Check(ctx); err != nil {
		return Model{}, err
	}
	requestCtx, cancel := context.WithCancel(ctx)
	selected := *cfg.Backend
	guard := func(ctx context.Context) error {
		current, err := config.Load(baseDir)
		if err != nil {
			return err
		}
		kind, err := config.Store(current)
		if err != nil {
			return err
		}
		if kind != config.StoreBackend || current.Backend == nil || *current.Backend != selected {
			return fmt.Errorf("configured backend project changed; restart the monitor")
		}
		active, err := backendstore.Open(ctx, baseDir, worktreeDir)
		if err != nil {
			return err
		}
		if active.SessionID != connection.SessionID || active.Worktree != connection.Worktree || active.Branch != connection.Branch {
			return fmt.Errorf("monitor session or worktree changed; restart the monitor")
		}
		return nil
	}
	key := sha256.Sum256([]byte(connection.Worktree))
	preferences := &localMonitorPreferences{ctx: requestCtx, path: filepath.Join(connection.StateDirectory, "monitor", fmt.Sprintf("%x.json", key))}
	prefs, err := preferences.Load()
	if err != nil {
		cancel()
		return Model{}, err
	}
	m := NewModel(nil, connection.SessionID, max(interval, MinRefreshInterval), version, baseDir)
	m.notesLifetimeCancel()
	m.notesLifetimeCancel = cancel
	m.NotesContext = requestCtx
	m.preferences = preferences
	m.PaneHeights = prefs.PaneHeights
	m.DataSource = NewBackendDataSource(requestCtx, connection, guard)
	m.BoardSource = NewBackendBoardSource(requestCtx, connection, guard, preferences)
	m.syncRuntime = newSyncRuntime(nil, SyncOptions{Disabled: true}, func() error { cancel(); return nil })
	return m, nil
}

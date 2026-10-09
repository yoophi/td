package monitor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/ghcontext"
	"github.com/marcus/td/internal/models"
)

// Local screen choices are scoped by repository + worktree on this device,
// independent of actor/session rotation. No project config or carrier is saved.
type localMonitorPreferences struct {
	ctx  context.Context
	path string
}
type monitorPreferences struct {
	Version            int                `json:"version"`
	PaneHeights        [3]float64         `json:"pane_heights"`
	Filter             config.FilterState `json:"filter"`
	GettingStartedSeen bool               `json:"getting_started_seen,omitempty"`
	LastBoardID        string             `json:"last_board_id,omitempty"`
	BoardViews         map[string]string  `json:"board_views,omitempty"`
}

func monitorPreferencesForScope(ctx context.Context, scope ghcontext.Scope) *localMonitorPreferences {
	key := sha256.Sum256([]byte(scope.Worktree))
	return &localMonitorPreferences{ctx: ctx, path: filepath.Join(scope.Directory, "monitor", fmt.Sprintf("%x.json", key))}
}
func (p *localMonitorPreferences) Load() (*monitorPreferences, error) {
	if p == nil {
		return nil, fmt.Errorf("device-local monitor preferences unavailable")
	}
	if err := p.ctx.Err(); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p.path)
	if os.IsNotExist(err) {
		return &monitorPreferences{Version: 1, PaneHeights: config.DefaultPaneHeights(), BoardViews: map[string]string{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var prefs monitorPreferences
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&prefs); err != nil {
		return nil, fmt.Errorf("invalid monitor preferences %s: %w", p.path, err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("invalid trailing monitor preferences data")
	}
	if err := prefs.validate(); err != nil {
		return nil, err
	}
	if prefs.BoardViews == nil {
		prefs.BoardViews = map[string]string{}
	}
	return &prefs, nil
}
func (p monitorPreferences) validate() error {
	if p.Version != 1 {
		return fmt.Errorf("unsupported monitor preferences version %d; existing file preserved", p.Version)
	}
	sum := 0.0
	for _, h := range p.PaneHeights {
		if h < 0.1 || h > 0.8 {
			return fmt.Errorf("invalid monitor pane height %v", h)
		}
		sum += h
	}
	if math.Abs(sum-1) > 0.0001 {
		return fmt.Errorf("monitor pane heights must sum to 1")
	}
	for _, mode := range p.BoardViews {
		if mode != "backlog" && mode != "swimlanes" {
			return fmt.Errorf("invalid saved monitor board view %q", mode)
		}
	}
	return nil
}
func (p *localMonitorPreferences) Update(change func(*monitorPreferences)) error {
	if p == nil {
		return fmt.Errorf("device-local monitor preferences unavailable")
	}
	if err := p.ctx.Err(); err != nil {
		return err
	}
	dir := filepath.Dir(p.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	return config.WithLocalStateLock(dir, func() error {
		prefs, err := p.Load()
		if err != nil {
			return err
		}
		change(prefs)
		if err := prefs.validate(); err != nil {
			return err
		}
		data, err := json.MarshalIndent(prefs, "", "  ")
		if err != nil {
			return err
		}
		tmp, err := os.CreateTemp(dir, "monitor-*.tmp")
		if err != nil {
			return err
		}
		defer func() { _ = os.Remove(tmp.Name()) }()
		if _, err := tmp.Write(data); err != nil {
			_ = tmp.Close()
			return err
		}
		if err := tmp.Close(); err != nil {
			return err
		}
		if err := p.ctx.Err(); err != nil {
			return err
		}
		return os.Rename(tmp.Name(), p.path)
	})
}
func (s *GitHubBoardSource) displayBoard(board models.Board) (*models.Board, error) {
	// Never import another device's shared last-view clock into local navigation.
	board.LastViewedAt = nil
	if s.preferences == nil {
		return &board, nil
	}
	prefs, err := s.preferences.Load()
	if err != nil {
		return nil, err
	}
	if mode := prefs.BoardViews[board.ID]; mode != "" {
		board.ViewMode = mode
	}
	return &board, nil
}

type MonitorPreferencesErrorMsg struct{ Error error }

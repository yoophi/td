// Package ghcontext keeps device-local identity and focus separate from shared
// GitHub issue data. It never opens an SQLite issue database.
package ghcontext

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/session"
	"github.com/marcus/td/internal/workdir"
)

type State struct {
	WorkSessions      []WorkSession     `json:"work_sessions,omitempty"`
	ActiveWorkSession string            `json:"active_work_session,omitempty"`
	History           []session.Session `json:"history,omitempty"`
	Version           int               `json:"version"`
	Session           session.Session   `json:"session"`
	Focus             string            `json:"focus,omitempty"`
}

type Scope struct{ Directory, Path, Branch, Repo, Worktree string }

func Resolve(ctx context.Context, dir, repo string) (Scope, error) {
	info, err := workdir.WorktreeForPath(dir)
	if err != nil {
		return Scope{}, err
	}
	command := exec.CommandContext(ctx, "git", "-C", info.WorktreeRoot, "symbolic-ref", "--quiet", "--short", "HEAD")
	branch, err := command.Output()
	if err != nil {
		branch, err = exec.CommandContext(ctx, "git", "-C", info.WorktreeRoot, "rev-parse", "--short", "HEAD").Output()
	}
	if err != nil {
		return Scope{}, fmt.Errorf("resolve GitHub session branch: %w", err)
	}
	home, err := os.UserConfigDir()
	if err != nil {
		return Scope{}, err
	}
	hostname, err := os.Hostname()
	if err != nil {
		return Scope{}, err
	}
	fingerprint := session.GetAgentFingerprint()
	// Preserve the raw explicit identity: sanitized/truncated names can collide.
	identity, _ := json.Marshal([]any{hostname, repo, info.WorktreeRoot, strings.TrimSpace(string(branch)), fingerprint, os.Getenv("TD_CONTEXT_ID"), os.Getenv("TERM_SESSION_ID"), os.Getenv("TMUX_PANE")})
	repoKey := sha256.Sum256([]byte(strings.ToLower(repo)))
	key := sha256.Sum256(identity)
	directory := filepath.Join(home, "td", "gh-contexts", fmt.Sprintf("%x", repoKey))
	return Scope{Directory: directory, Path: filepath.Join(directory, fmt.Sprintf("%x.json", key)), Branch: strings.TrimSpace(string(branch)), Repo: repo, Worktree: info.WorktreeRoot}, nil
}

// ResolveWeb isolates the shared web identity from the launching terminal or
// agent. Each repository/worktree/branch has a stable, separate web session.
func ResolveWeb(ctx context.Context, dir, repo string) (Scope, error) {
	scope, err := Resolve(ctx, dir, repo)
	if err != nil {
		return Scope{}, err
	}
	identity, _ := json.Marshal([]string{"td-serve-web", strings.ToLower(repo), scope.Worktree, scope.Branch})
	key := sha256.Sum256(identity)
	scope.Path = filepath.Join(scope.Directory, fmt.Sprintf("%x.json", key))
	return scope, nil
}

func (s Scope) Update(ctx context.Context, change func(*State) error) (*State, error) {
	var state State
	err := config.WithLocalStateLock(s.Directory, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		data, err := os.ReadFile(s.Path)
		if err == nil {
			if err := json.Unmarshal(data, &state); err != nil {
				return fmt.Errorf("invalid local GitHub context (preserved): %w", err)
			}
			if state.Version != 1 || state.Session.ID == "" {
				return fmt.Errorf("unsupported local GitHub context version or identity")
			}
		} else if !os.IsNotExist(err) {
			return err
		} else {
			state = State{Version: 1}
			s.NewSession(&state)
		}
		if change != nil {
			if err := change(&state); err != nil {
				return err
			}
		}
		state.Session.LastActivity = time.Now().UTC()
		data, err = json.MarshalIndent(state, "", "  ")
		if err != nil {
			return err
		}
		file, err := os.CreateTemp(s.Directory, ".context-*")
		if err != nil {
			return err
		}
		defer func() { _ = os.Remove(file.Name()) }()
		if _, err := file.Write(data); err != nil {
			_ = file.Close()
			return err
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
		return os.Rename(file.Name(), s.Path)
	})
	return &state, err
}

func (s Scope) NewSession(state *State) {
	previous := state.Session.ID
	if previous != "" {
		state.History = append(state.History, state.Session)
	}
	fp := session.GetAgentFingerprint()
	state.Session = session.Session{ID: "ses_" + rand.Text(), PreviousSessionID: previous, Branch: s.Branch, AgentType: string(fp.Type), AgentPID: fp.PID, StartedAt: time.Now().UTC(), WorktreeRoot: s.Worktree, MatchContextID: os.Getenv("TD_CONTEXT_ID")}
	state.Focus = ""
	state.ActiveWorkSession = ""
}

// List returns device-local contexts for this repository. Shared issue activity
// is stored independently on GitHub, not inferred from these local files.
func (s Scope) List() ([]session.Session, error) {
	paths, err := filepath.Glob(filepath.Join(s.Directory, "*.json"))
	if err != nil {
		return nil, err
	}
	result := make([]session.Session, 0, len(paths))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var state State
		if err := json.Unmarshal(data, &state); err != nil {
			return nil, fmt.Errorf("invalid local context %s: %w", path, err)
		}
		if state.Version != 1 || state.Session.ID == "" {
			return nil, fmt.Errorf("unsupported local context %s", path)
		}
		result = append(result, state.History...)
		result = append(result, state.Session)
	}
	return result, nil
}

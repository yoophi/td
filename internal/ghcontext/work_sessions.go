package ghcontext

import (
	"crypto/rand"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/marcus/td/internal/models"
)

// WorkSession is a device-local bundle. Activities bearing its ID are stored
// independently on GitHub; removing a local bundle must never remove comments.
type WorkSession struct {
	models.WorkSession
	Issues []string `json:"issues"`
}

func (s *State) WorkSession(id string) (*WorkSession, error) {
	for i := range s.WorkSessions {
		if s.WorkSessions[i].ID == id {
			return &s.WorkSessions[i], nil
		}
	}
	return nil, fmt.Errorf("work session not found in this local context: %s", id)
}

func (s *State) CurrentWorkSession() (*WorkSession, error) {
	if s.ActiveWorkSession == "" {
		return nil, fmt.Errorf("no active work session; run 'td ws start <name>' first")
	}
	ws, err := s.WorkSession(s.ActiveWorkSession)
	if err != nil {
		return nil, err
	}
	if ws.EndedAt != nil || ws.SessionID != s.Session.ID {
		return nil, fmt.Errorf("invalid active work session: %s", ws.ID)
	}
	return ws, nil
}

func (s *State) StartWorkSession(name, sha, worktree, repoRoot string) (*WorkSession, error) {
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("work session name must not be empty")
	}
	if s.ActiveWorkSession != "" {
		return nil, fmt.Errorf("work session already active: %s", s.ActiveWorkSession)
	}
	ws := WorkSession{WorkSession: models.WorkSession{ID: "ws_" + rand.Text(), Name: name, SessionID: s.Session.ID, StartedAt: time.Now().UTC(), StartSHA: sha, WorktreeRoot: worktree, RepoRoot: repoRoot}, Issues: []string{}}
	s.WorkSessions = append(s.WorkSessions, ws)
	s.ActiveWorkSession = ws.ID
	return &s.WorkSessions[len(s.WorkSessions)-1], nil
}

// TagWorkSession only accepts canonical issue IDs already verified by the
// GitHub caller. It is deliberately separate from remote workflow transitions.
func (s *State) TagWorkSession(id string) error {
	ws, err := s.CurrentWorkSession()
	if err != nil {
		return err
	}
	if !strings.HasPrefix(id, "gh-") || len(id) == 3 {
		return fmt.Errorf("canonical GitHub issue ID required: %s", id)
	}
	if !slices.Contains(ws.Issues, id) {
		ws.Issues = append(ws.Issues, id)
	}
	return nil
}

func (s *State) UntagWorkSession(id string) error {
	ws, err := s.CurrentWorkSession()
	if err != nil {
		return err
	}
	ws.Issues = slices.DeleteFunc(ws.Issues, func(value string) bool { return value == id })
	return nil
}

func (s *State) EndWorkSession(sha string) (*WorkSession, error) {
	ws, err := s.CurrentWorkSession()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	ws.EndedAt, ws.EndSHA = &now, sha
	s.ActiveWorkSession = ""
	return ws, nil
}

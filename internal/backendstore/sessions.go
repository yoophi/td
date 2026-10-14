package backendstore

import (
	"context"
	"fmt"
	"time"
)

type Session struct {
	ID                string    `json:"id"`
	PreviousSessionID *string   `json:"previous_session_id"`
	LastActivity      time.Time `json:"last_activity"`
}

func (c *Connection) ListSessions(ctx context.Context) ([]Session, error) {
	path, err := scopedPath(c.ProjectID, c.SessionID)
	if err != nil {
		return nil, err
	}
	var out struct {
		Sessions []Session `json:"sessions"`
		Current  string    `json:"current_session_id"`
	}
	if err := c.Client.request(ctx, "GET", path+"/sessions", c.SessionID, "", nil, &out); err != nil {
		return nil, err
	}
	if out.Sessions == nil || out.Current != c.SessionID {
		return nil, fmt.Errorf("invalid backend session list")
	}
	seen := map[string]bool{}
	for _, session := range out.Sessions {
		if !validSessionID(session.ID) || seen[session.ID] || session.LastActivity.IsZero() || session.PreviousSessionID != nil && !validSessionID(*session.PreviousSessionID) {
			return nil, fmt.Errorf("invalid backend session identity")
		}
		seen[session.ID] = true
	}
	if !seen[c.SessionID] {
		return nil, fmt.Errorf("backend omitted current session")
	}
	return out.Sessions, nil
}

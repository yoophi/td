package backendstore

import (
	"context"
	"fmt"
	"regexp"
	"time"
)

var workSessionIDPattern = regexp.MustCompile(`^ws-[0-9a-f]{16}$`)

func (c *Connection) ActiveWorkIssueIDs(ctx context.Context) ([]string, error) {
	path, e := scopedPath(c.ProjectID, c.SessionID)
	if e != nil {
		return nil, e
	}
	var out struct {
		Sessions []struct {
			ID        string     `json:"id"`
			SessionID string     `json:"session_id"`
			EndedAt   *time.Time `json:"ended_at"`
		} `json:"work_sessions"`
	}
	if e = c.Client.request(ctx, "GET", path+"/work-sessions", c.SessionID, "", nil, &out); e != nil {
		return nil, e
	}
	id := ""
	for _, s := range out.Sessions {
		if s.SessionID == c.SessionID && s.EndedAt == nil {
			if id != "" || !workSessionIDPattern.MatchString(s.ID) {
				return nil, fmt.Errorf("invalid active backend work session")
			}
			id = s.ID
		}
	}
	if id == "" {
		return []string{}, nil
	}
	var detail struct {
		IDs []string `json:"issue_ids"`
	}
	if e = c.Client.request(ctx, "GET", path+"/work-sessions/"+id, c.SessionID, "", nil, &detail); e != nil {
		return nil, e
	}
	for _, id := range detail.IDs {
		if !issueIDPattern.MatchString(id) {
			return nil, fmt.Errorf("invalid backend work issue ID")
		}
	}
	return detail.IDs, nil
}

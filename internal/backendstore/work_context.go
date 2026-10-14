package backendstore

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"github.com/marcus/td/internal/models"
)

var workSessionIDPattern = regexp.MustCompile(`^ws-[0-9a-f]{16}$`)

type WorkSession struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	SessionID string     `json:"session_id"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at"`
	StartSHA  string     `json:"start_sha"`
	EndSHA    string     `json:"end_sha"`
}

type WorkSessionDetail struct {
	WorkSession WorkSession `json:"work_session"`
	IssueIDs    []string    `json:"issue_ids"`
}

func ValidateWorkSession(row WorkSession) error {
	if !workSessionIDPattern.MatchString(row.ID) || !validSessionID(row.SessionID) || row.Name == "" || row.StartedAt.IsZero() {
		return fmt.Errorf("invalid backend work session")
	}
	return nil
}

func (c *Connection) WorkSessions(ctx context.Context) ([]WorkSession, error) {
	path, e := scopedPath(c.ProjectID, c.SessionID)
	if e != nil {
		return nil, e
	}
	var out struct {
		Sessions []WorkSession `json:"work_sessions"`
	}
	if e = c.Client.request(ctx, "GET", path+"/work-sessions", c.SessionID, "", nil, &out); e != nil {
		return nil, e
	}
	if out.Sessions == nil {
		return nil, fmt.Errorf("invalid backend work session list")
	}
	seen := map[string]bool{}
	for _, row := range out.Sessions {
		if ValidateWorkSession(row) != nil || seen[row.ID] {
			return nil, fmt.Errorf("invalid backend work session list")
		}
		seen[row.ID] = true
	}
	return out.Sessions, nil
}

func (c *Connection) WorkSession(ctx context.Context, id string) (WorkSessionDetail, error) {
	path, e := scopedPath(c.ProjectID, c.SessionID)
	if e != nil {
		return WorkSessionDetail{}, e
	}
	if !workSessionIDPattern.MatchString(id) {
		return WorkSessionDetail{}, fmt.Errorf("invalid backend work session ID")
	}
	var out WorkSessionDetail
	if e = c.Client.request(ctx, "GET", path+"/work-sessions/"+id, c.SessionID, "", nil, &out); e != nil {
		return WorkSessionDetail{}, e
	}
	if ValidateWorkSession(out.WorkSession) != nil || out.WorkSession.ID != id || out.IssueIDs == nil {
		return WorkSessionDetail{}, fmt.Errorf("invalid backend work session detail")
	}
	seen := map[string]bool{}
	for _, issueID := range out.IssueIDs {
		if !issueIDPattern.MatchString(issueID) || seen[issueID] {
			return WorkSessionDetail{}, fmt.Errorf("invalid backend work session issue IDs")
		}
		seen[issueID] = true
	}
	return out, nil
}

func (c *Connection) WorkSessionActivity(ctx context.Context, id string) ([]models.Activity, error) {
	path, e := scopedPath(c.ProjectID, c.SessionID)
	if e != nil {
		return nil, e
	}
	if !workSessionIDPattern.MatchString(id) {
		return nil, fmt.Errorf("invalid backend work session ID")
	}
	var out struct {
		Activity []models.Activity `json:"activity"`
	}
	if e = c.Client.request(ctx, "GET", path+"/work-sessions/"+id+"/activity", c.SessionID, "", nil, &out); e != nil {
		return nil, e
	}
	if out.Activity == nil {
		return nil, fmt.Errorf("invalid backend work session activity list")
	}
	seen := map[string]bool{}
	for _, row := range out.Activity {
		if row.ID == "" || seen[row.ID] || !issueIDPattern.MatchString(row.IssueID) || row.WorkSessionID != id || row.CreatedAt.IsZero() || row.UpdatedAt.IsZero() {
			return nil, fmt.Errorf("invalid backend work session activity")
		}
		seen[row.ID] = true
	}
	return out.Activity, nil
}

func (c *Connection) WorkSessionLogs(ctx context.Context, id string) ([]models.Log, error) {
	path, e := scopedPath(c.ProjectID, c.SessionID)
	if e != nil {
		return nil, e
	}
	if !workSessionIDPattern.MatchString(id) {
		return nil, fmt.Errorf("invalid backend work session ID")
	}
	var out struct {
		Logs []models.Log `json:"logs"`
	}
	if e = c.Client.request(ctx, "GET", path+"/work-sessions/"+id+"/logs", c.SessionID, "", nil, &out); e != nil {
		return nil, e
	}
	if out.Logs == nil {
		return nil, fmt.Errorf("invalid backend work session logs")
	}
	seen := map[string]bool{}
	for _, row := range out.Logs {
		if row.ID == "" || seen[row.ID] || row.IssueID != "" || row.WorkSessionID != id || !validSessionID(row.SessionID) || row.Message == "" || row.Timestamp.IsZero() {
			return nil, fmt.Errorf("invalid backend work session log")
		}
		seen[row.ID] = true
	}
	return out.Logs, nil
}

func (c *Connection) ActiveWorkSessionID(ctx context.Context) (string, error) {
	rows, e := c.WorkSessions(ctx)
	if e != nil {
		return "", e
	}
	id := ""
	for _, row := range rows {
		if row.SessionID == c.SessionID && row.EndedAt == nil {
			if id != "" || !workSessionIDPattern.MatchString(row.ID) {
				return "", fmt.Errorf("invalid active backend work session")
			}
			id = row.ID
		}
	}
	return id, nil
}

func (c *Connection) ActiveWorkIssueIDs(ctx context.Context) ([]string, error) {
	id, e := c.ActiveWorkSessionID(ctx)
	if e != nil {
		return nil, e
	}
	if id == "" {
		return []string{}, nil
	}
	detail, e := c.WorkSession(ctx, id)
	if e != nil {
		return nil, e
	}
	return detail.IssueIDs, nil
}

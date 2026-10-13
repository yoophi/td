package backendstore

import (
	"context"
	"fmt"
	"time"

	"github.com/marcus/td/internal/models"
)

type Comment struct {
	ID        string    `json:"id"`
	IssueID   string    `json:"issue_id"`
	SessionID string    `json:"session_id"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
}

type Detail struct {
	Issue         Issue                    `json:"issue"`
	Logs          []models.Log             `json:"logs"`
	LatestHandoff *models.Handoff          `json:"latest_handoff"`
	Children      []Issue                  `json:"children"`
	Dependencies  []models.IssueDependency `json:"dependencies"`
	BlockedBy     []models.IssueDependency `json:"blocked_by"`
	Files         []models.IssueFile       `json:"files"`
	Comments      []Comment                `json:"comments"`
}

func (c *Client) Detail(ctx context.Context, project, session, id string) (Detail, error) {
	var out Detail
	path, e := scopedPath(project, session)
	if e != nil {
		return out, e
	}
	if !issueIDPattern.MatchString(id) {
		return out, fmt.Errorf("invalid backend issue ID")
	}
	e = c.request(ctx, "GET", path+"/issues/"+id+"?with=reviews", session, "", nil, &out)
	if e == nil {
		e = validateIssue(out.Issue)
	}
	if e == nil && out.Issue.ID != id {
		e = fmt.Errorf("backend returned a different issue")
	}
	if e == nil {
		for _, child := range out.Children {
			if e = validateIssue(child); e != nil {
				break
			}
		}
	}
	return out, e
}
func (c *Connection) Detail(ctx context.Context, id string) (Detail, error) {
	return c.Client.Detail(ctx, c.ProjectID, c.SessionID, id)
}
func (c *Connection) Focus(ctx context.Context) (string, error) {
	path, e := scopedPath(c.ProjectID, c.SessionID)
	if e != nil {
		return "", e
	}
	var out struct {
		ID *string `json:"focused_issue_id"`
	}
	if e = c.Client.request(ctx, "GET", path+"/focus", c.SessionID, "", nil, &out); e != nil {
		return "", e
	}
	if out.ID == nil {
		return "", nil
	}
	if !issueIDPattern.MatchString(*out.ID) {
		return "", fmt.Errorf("invalid backend focused issue ID")
	}
	return *out.ID, nil
}

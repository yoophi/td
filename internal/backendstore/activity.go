package backendstore

import (
	"context"
	"fmt"

	"github.com/marcus/td/internal/models"
)

func (c *Connection) ListIssuesIncludingDeleted(ctx context.Context) ([]Issue, error) {
	return c.Client.ListIssuesIncludingDeleted(ctx, c.ProjectID, c.SessionID)
}

// IssueActivityIncludingDeleted keeps shared handoff history available after
// an issue is archived. Individual deleted activity rows remain excluded.
func (c *Connection) IssueActivityIncludingDeleted(ctx context.Context, id string) ([]models.Activity, error) {
	path, err := scopedPath(c.ProjectID, c.SessionID)
	if err != nil {
		return nil, err
	}
	if !ValidIssueID(id) {
		return nil, fmt.Errorf("invalid backend issue ID")
	}
	var out struct {
		Activity []models.Activity `json:"activity"`
	}
	if err := c.Client.request(ctx, "GET", path+"/issues/"+id+"/activity?with_deleted=true", c.SessionID, "", nil, &out); err != nil {
		return nil, err
	}
	if out.Activity == nil {
		return nil, fmt.Errorf("invalid backend issue activity list")
	}
	seen := map[string]bool{}
	for _, row := range out.Activity {
		if row.ID == "" || seen[row.ID] || row.IssueID != id || row.Kind == "" || row.CreatedAt.IsZero() || row.UpdatedAt.IsZero() {
			return nil, fmt.Errorf("invalid backend issue activity")
		}
		seen[row.ID] = true
	}
	return out.Activity, nil
}

package ghstore

import (
	"context"
	"encoding/json"
	"fmt"
)

// Collaborator preserves GitHub roles and effective base permissions. These
// are not td-sync owner/writer/reader roles or evidence of a direct grant.
type Collaborator struct {
	ID          int64           `json:"id"`
	Login       string          `json:"login"`
	RoleName    string          `json:"role_name,omitempty"`
	Permissions map[string]bool `json:"permissions,omitempty"`
}

// Collaborators uses one paginated collection, never one request per account.
func (c *Client) Collaborators(ctx context.Context) ([]Collaborator, error) {
	data, err := c.request(ctx, "GET", "/collaborators?affiliation=all&per_page=100", nil, true)
	if err != nil {
		return nil, fmt.Errorf("list GitHub collaborators: %w; this operation requires collaborator-list access, which may exceed issue access", err)
	}
	var pages []json.RawMessage
	if err := json.Unmarshal(data, &pages); err != nil || pages == nil {
		return nil, fmt.Errorf("invalid gh collaborator pages")
	}
	result := []Collaborator{}
	for _, page := range pages {
		var members []Collaborator
		if err := json.Unmarshal(page, &members); err != nil || members == nil {
			return nil, fmt.Errorf("invalid gh collaborator page")
		}
		for _, member := range members {
			if member.ID <= 0 || member.Login == "" {
				return nil, fmt.Errorf("invalid gh collaborator identity")
			}
			result = append(result, member)
		}
	}
	return result, nil
}

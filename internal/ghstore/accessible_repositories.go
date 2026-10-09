package ghstore

import (
	"context"
	"encoding/json"
	"fmt"
)

// This allowlist intentionally excludes raw repository response credentials
// and unrelated metadata (e.g. temp_clone_token).
type AccessibleRepository struct {
	ID          int64           `json:"id"`
	FullName    string          `json:"full_name"`
	Visibility  string          `json:"visibility"`
	HasIssues   *bool           `json:"has_issues"`
	Archived    *bool           `json:"archived"`
	Disabled    *bool           `json:"disabled"`
	Permissions map[string]bool `json:"permissions,omitempty"`
}

func (c *Client) AccessibleRepositories(ctx context.Context) ([]AccessibleRepository, error) {
	path := "user/repos?visibility=all&affiliation=owner,collaborator,organization_member&sort=full_name&direction=asc&per_page=100"
	data, err := c.requestAPI(ctx, "GET", path, path, nil, true)
	if err != nil {
		return nil, err
	}
	var pages []json.RawMessage
	if err := json.Unmarshal(data, &pages); err != nil || pages == nil {
		return nil, fmt.Errorf("invalid gh accessible repository pages")
	}
	repositories := []AccessibleRepository{}
	for _, page := range pages {
		var rows []AccessibleRepository
		if err := json.Unmarshal(page, &rows); err != nil || rows == nil {
			return nil, fmt.Errorf("invalid gh accessible repository page")
		}
		for _, repo := range rows {
			if repo.ID <= 0 || !repositoryPattern.MatchString(repo.FullName) || repo.HasIssues == nil || repo.Archived == nil || repo.Disabled == nil {
				return nil, fmt.Errorf("invalid gh accessible repository identity/state")
			}
			switch repo.Visibility {
			case "public", "private", "internal":
			default:
				return nil, fmt.Errorf("invalid gh accessible repository visibility")
			}
			repositories = append(repositories, repo)
		}
	}
	return repositories, nil
}

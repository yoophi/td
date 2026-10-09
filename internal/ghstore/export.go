package ghstore

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/marcus/td/internal/models"
)

// ExportedIssue contains shared data only. Reads are not an atomic snapshot.
type ExportedIssue struct {
	Version     int               `json:"version"`
	Repository  string            `json:"repository"`
	Record      Record            `json:"record"`
	Body        string            `json:"body"`
	NativeState string            `json:"native_state"`
	Author      string            `json:"author"`
	Activity    []models.Activity `json:"activity"`
}

func (c *Client) ExportIssues(ctx context.Context, all, includeActivity bool) ([]ExportedIssue, error) {
	if includeActivity {
		snapshot, err := c.ReadSnapshot(WithFreshSnapshot(ctx), true)
		if err != nil {
			return nil, err
		}
		result := []ExportedIssue{}
		records := map[int]Record{}
		for _, r := range snapshot.Issues(true) {
			records[r.Number] = r
		}
		for _, entry := range snapshot.entries {
			item := entry.Issue
			r, ok := records[item.Number]
			if !ok || (!all && (item.State != "open" || r.DeletedAt != nil)) {
				continue
			}
			activity, err := snapshot.Activity(r.ID)
			if err != nil {
				return nil, err
			}
			result = append(result, ExportedIssue{Version: 1, Repository: c.repo, Record: r, Body: item.Body, NativeState: item.State, Author: item.User.Login, Activity: activity})
		}
		return result, nil
	}
	state := "open"
	if all {
		state = "all"
	}
	data, err := c.request(ctx, "GET", "/issues?state="+state+"&per_page=100", nil, true)
	if err != nil {
		return nil, err
	}
	var pages [][]apiIssue
	if err := json.Unmarshal(data, &pages); err != nil {
		return nil, fmt.Errorf("invalid export issue list: %w", err)
	}
	result := []ExportedIssue{}
	seen := map[int]bool{}
	for _, page := range pages {
		for _, item := range page {
			if len(item.PullRequest) > 0 && string(item.PullRequest) != "null" {
				continue
			}
			if seen[item.Number] {
				return nil, fmt.Errorf("export pagination repeated issue %d", item.Number)
			}
			seen[item.Number] = true
			r, err := item.record()
			if err != nil {
				return nil, err
			}
			if r.meta.EntityKind != "" && r.meta.EntityKind != "issue" {
				continue
			}
			if !all && r.DeletedAt != nil {
				continue
			}
			activity := []models.Activity{}
			result = append(result, ExportedIssue{Version: 1, Repository: c.repo, Record: *r, Body: item.Body, NativeState: item.State, Author: item.User.Login, Activity: activity})
		}
	}
	return result, nil
}

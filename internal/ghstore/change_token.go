package ghstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// ChangeToken observes repository issues (including deleted/auxiliary entities)
// and their comments. It is a content fingerprint, not an atomic revision or a
// replay cursor. Failed or corrupt reads never produce a successful token.
func (c *Client) ChangeToken(ctx context.Context) (string, error) {
	data, err := c.request(ctx, "GET", "/issues?state=all&per_page=100", nil, true)
	if err != nil {
		return "", err
	}
	var pages [][]apiIssue
	if err := json.Unmarshal(data, &pages); err != nil {
		return "", fmt.Errorf("invalid change-token issue list: %w", err)
	}
	type entry struct {
		Issue    apiIssue
		Comments []apiComment
	}
	entries := []entry{}
	issues := map[int]bool{}
	comments := map[int64]bool{}
	for _, page := range pages {
		for _, item := range page {
			if len(item.PullRequest) > 0 && string(item.PullRequest) != "null" {
				continue
			}
			record, err := item.record()
			if err != nil {
				return "", err
			}
			if issues[item.Number] {
				return "", fmt.Errorf("change-token pagination repeated issue %d", item.Number)
			}
			issues[item.Number] = true
			sort.Slice(item.Labels, func(i, j int) bool { return item.Labels[i].Name < item.Labels[j].Name })
			data, err := c.request(ctx, "GET", fmt.Sprintf("/issues/%d/comments?per_page=100", item.Number), nil, true)
			if err != nil {
				return "", err
			}
			var activityPages [][]apiComment
			if err := json.Unmarshal(data, &activityPages); err != nil {
				return "", fmt.Errorf("invalid change-token comments: %w", err)
			}
			all := []apiComment{}
			operations := map[string]bool{}
			for _, page := range activityPages {
				for _, comment := range page {
					if comments[comment.ID] {
						return "", fmt.Errorf("change-token pagination repeated comment %d", comment.ID)
					}
					comments[comment.ID] = true
					a, err := decodeActivity(comment, record.ID)
					if err != nil {
						return "", err
					}
					if a.OperationID != "" {
						if operations[a.OperationID] {
							return "", fmt.Errorf("duplicate activity operation %s", a.OperationID)
						}
						operations[a.OperationID] = true
					}
					all = append(all, comment)
				}
			}
			sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
			entries = append(entries, entry{Issue: item, Comments: all})
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Issue.Number < entries[j].Issue.Number })
	encoded, err := json.Marshal(entries)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(encoded)
	return "gh-" + hex.EncodeToString(hash[:]), nil
}

package ghstore

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"time"
)

// readSnapshotIncremental merges updated entities into a complete baseline.
// The updated feeds have no deletion tombstones: a regular full reconciliation
// is mandatory. Neither a partial page nor invalid metadata advances the cursor.
func (c *Client) readSnapshotIncremental(ctx context.Context, baseline *Snapshot) (*Snapshot, error) {
	started := time.Now().UTC()
	if started.Sub(baseline.FullReconciledAt()) >= snapshotFullReconciliationInterval {
		return c.readSnapshotRemote(ctx, baseline.fullHistory)
	}
	since := url.QueryEscape(baseline.ObservedAt().Add(-2 * time.Second).UTC().Format(time.RFC3339))
	data, err := c.request(ctx, "GET", "/issues?state=all&per_page=100&sort=updated&direction=asc&since="+since, nil, true)
	if err != nil {
		return nil, err
	}
	var pages [][]apiIssue
	if err := json.Unmarshal(data, &pages); err != nil {
		return nil, fmt.Errorf("invalid incremental issue list: %w", err)
	}
	if pages == nil {
		return nil, fmt.Errorf("incremental issue list is not a paginated array")
	}
	deltaIssues := map[int]apiIssue{}
	for _, page := range pages {
		if page == nil {
			return nil, fmt.Errorf("incremental issue page is null")
		}
		for _, item := range page {
			if item.Number <= 0 || item.UpdatedAt.IsZero() {
				return nil, fmt.Errorf("invalid incremental issue identity/timestamp")
			}
			if len(item.PullRequest) == 0 || string(item.PullRequest) == "null" {
				if _, err := item.record(); err != nil {
					return nil, err
				}
			}
			if old, ok := deltaIssues[item.Number]; ok {
				if item.UpdatedAt.Before(old.UpdatedAt) {
					continue
				}
				if item.UpdatedAt.Equal(old.UpdatedAt) && !reflect.DeepEqual(old, item) {
					return nil, fmt.Errorf("conflicting incremental versions of issue %d", item.Number)
				}
			}
			deltaIssues[item.Number] = item
		}
	}
	issues := map[int]apiIssue{}
	comments := map[int64]apiComment{}
	prs := map[int]bool{}
	for _, number := range baseline.pullRequests {
		prs[number] = true
	}
	for _, entry := range baseline.entries {
		issues[entry.Issue.Number] = entry.Issue
		for _, comment := range entry.Comments {
			comments[comment.ID] = comment
		}
	}
	for number, item := range deltaIssues {
		if len(item.PullRequest) > 0 && string(item.PullRequest) != "null" {
			prs[number] = true
			delete(issues, number)
		} else {
			delete(prs, number)
			// A feed read later than the baseline can have a same-second edit.
			// Older versions never replace a newer baseline observation.
			if old, ok := issues[number]; !ok || !item.UpdatedAt.Before(old.UpdatedAt) {
				issues[number] = item
			}
		}
	}
	if baseline.fullHistory && len(issues) > 0 {
		data, err = c.request(ctx, "GET", "/issues/comments?per_page=100&sort=updated&direction=asc&since="+since, nil, true)
		if err != nil {
			return nil, err
		}
		var commentPages [][]apiComment
		if err := json.Unmarshal(data, &commentPages); err != nil {
			return nil, fmt.Errorf("invalid incremental comments: %w", err)
		}
		if commentPages == nil {
			return nil, fmt.Errorf("incremental comments are not a paginated array")
		}
		deltaComments := map[int64]apiComment{}
		unknownOwner := false
		for _, page := range commentPages {
			if page == nil {
				return nil, fmt.Errorf("incremental comment page is null")
			}
			for _, comment := range page {
				if comment.ID <= 0 || comment.UpdatedAt.IsZero() {
					return nil, fmt.Errorf("invalid incremental comment identity/timestamp")
				}
				number, err := c.commentIssueNumber(comment.IssueURL)
				if err != nil {
					return nil, err
				}
				if prs[number] {
					continue
				}
				if _, ok := issues[number]; !ok {
					unknownOwner = true
					continue
				}
				if _, err := decodeActivity(comment, fmt.Sprintf("gh-%d", number)); err != nil {
					return nil, err
				}
				if old, ok := deltaComments[comment.ID]; ok {
					if old.IssueURL != comment.IssueURL {
						return nil, fmt.Errorf("incremental comment %d changed issue identity", comment.ID)
					}
					if comment.UpdatedAt.Before(old.UpdatedAt) {
						continue
					}
					if comment.UpdatedAt.Equal(old.UpdatedAt) && !reflect.DeepEqual(old, comment) {
						return nil, fmt.Errorf("conflicting incremental versions of comment %d", comment.ID)
					}
				}
				deltaComments[comment.ID] = comment
			}
		}
		// A previously unseen PR/issue may own a comment without appearing in the
		// issue delta. Resolve ownership with one full sweep, never per-issue GETs.
		if unknownOwner {
			return c.readSnapshotRemote(ctx, baseline.fullHistory)
		}
		for id, comment := range deltaComments {
			if old, ok := comments[id]; ok && old.IssueURL != comment.IssueURL {
				return nil, fmt.Errorf("incremental comment %d changed issue identity", id)
			}
			if old, ok := comments[id]; !ok || !comment.UpdatedAt.Before(old.UpdatedAt) {
				comments[id] = comment
			}
		}
	}
	mergedIssues := []apiIssue{}
	mergedComments := []apiComment{}
	for _, item := range issues {
		mergedIssues = append(mergedIssues, item)
	}
	for number := range prs {
		mergedIssues = append(mergedIssues, apiIssue{Number: number, PullRequest: json.RawMessage(`{}`)})
	}
	for _, comment := range comments {
		mergedComments = append(mergedComments, comment)
	}
	result, err := c.assembleSnapshot([][]apiIssue{mergedIssues}, [][]apiComment{mergedComments}, baseline.fullHistory)
	if err != nil {
		return nil, err
	}
	result.observedAt = started
	result.fullReconciledAt = baseline.FullReconciledAt()
	return result, nil
}

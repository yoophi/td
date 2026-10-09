package ghstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/marcus/td/internal/models"
)

// Snapshot is a complete read-only observation, not an atomic repository
// revision. Callers must still obtain fresh observations before mutations.
type Snapshot struct {
	records     []Record
	activities  map[string][]models.Activity
	entries     []snapshotEntry
	fullHistory bool
}

type snapshotEntry struct {
	Issue    apiIssue
	Comments []apiComment
}

// Issues returns tasks, excluding note/board carriers and optionally deleted
// tasks. Values are observations and must not be mutated by consumers.
func (s *Snapshot) Issues(includeDeleted bool) []Record {
	result := make([]Record, 0)
	for _, record := range s.records {
		if record.meta.EntityKind != "" && record.meta.EntityKind != "issue" {
			continue
		}
		if record.DeletedAt != nil && !includeDeleted {
			continue
		}
		result = append(result, record)
	}
	return result
}

// Activity returns already validated history without another network request.
func (s *Snapshot) Activity(id string) ([]models.Activity, error) {
	if !s.fullHistory {
		return nil, fmt.Errorf("snapshot does not contain complete activity")
	}
	activity, ok := s.activities[id]
	if !ok {
		return nil, fmt.Errorf("%s is absent from the snapshot", id)
	}
	return append([]models.Activity{}, activity...), nil
}

func (s *Snapshot) ChangeToken() (string, error) {
	if !s.fullHistory {
		return "", fmt.Errorf("change token requires complete activity")
	}
	encoded, err := json.Marshal(s.entries)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(encoded)
	return "gh-" + hex.EncodeToString(hash[:]), nil
}

// ReadSnapshot uses one paginated issue collection and, when requested, one
// paginated repository comment collection. No per-issue endpoint is visited.
// Every page must succeed before any snapshot is returned.
func (c *Client) ReadSnapshot(ctx context.Context, includeActivity bool) (*Snapshot, error) {
	data, err := c.request(ctx, "GET", "/issues?state=all&per_page=100", nil, true)
	if err != nil {
		return nil, err
	}
	var pages [][]apiIssue
	if err := json.Unmarshal(data, &pages); err != nil {
		return nil, fmt.Errorf("invalid snapshot issue list: %w", err)
	}
	if pages == nil {
		return nil, fmt.Errorf("snapshot issue list is not a paginated array")
	}
	s := &Snapshot{records: []Record{}, activities: map[string][]models.Activity{}, entries: []snapshotEntry{}, fullHistory: includeActivity}
	seen := map[int]bool{}
	prs := map[int]bool{}
	positions := map[int]int{}
	for _, page := range pages {
		if page == nil {
			return nil, fmt.Errorf("snapshot issue page is null")
		}
		for _, item := range page {
			if item.Number <= 0 || seen[item.Number] {
				return nil, fmt.Errorf("invalid or repeated snapshot issue %d", item.Number)
			}
			seen[item.Number] = true
			if len(item.PullRequest) > 0 && string(item.PullRequest) != "null" {
				prs[item.Number] = true
				continue
			}
			record, err := item.record()
			if err != nil {
				return nil, err
			}
			record.repository = c.repo
			sort.Slice(item.Labels, func(i, j int) bool { return item.Labels[i].Name < item.Labels[j].Name })
			positions[item.Number] = len(s.entries)
			s.records = append(s.records, *record)
			s.activities[record.ID] = []models.Activity{}
			s.entries = append(s.entries, snapshotEntry{Issue: item, Comments: []apiComment{}})
		}
	}
	if includeActivity && len(s.entries) > 0 {
		data, err := c.request(ctx, "GET", "/issues/comments?per_page=100", nil, true)
		if err != nil {
			return nil, err
		}
		var pages [][]apiComment
		if err := json.Unmarshal(data, &pages); err != nil {
			return nil, fmt.Errorf("invalid snapshot comments: %w", err)
		}
		if pages == nil {
			return nil, fmt.Errorf("snapshot comments are not a paginated array")
		}
		seenComments := map[int64]bool{}
		operations := map[int]map[string]bool{}
		for _, page := range pages {
			if page == nil {
				return nil, fmt.Errorf("snapshot comment page is null")
			}
			for _, comment := range page {
				if comment.ID <= 0 || seenComments[comment.ID] {
					return nil, fmt.Errorf("invalid or repeated snapshot comment %d", comment.ID)
				}
				seenComments[comment.ID] = true
				number, err := c.commentIssueNumber(comment.IssueURL)
				if err != nil {
					return nil, err
				}
				if prs[number] {
					continue
				}
				pos, ok := positions[number]
				if !ok {
					return nil, fmt.Errorf("comment %d refers to issue %d absent from the snapshot; repository changed during observation, retry the read", comment.ID, number)
				}
				id := s.records[pos].ID
				activity, err := decodeActivity(comment, id)
				if err != nil {
					return nil, err
				}
				if activity.OperationID != "" {
					if operations[number] == nil {
						operations[number] = map[string]bool{}
					}
					if operations[number][activity.OperationID] {
						return nil, fmt.Errorf("duplicate activity operation %s on %s", activity.OperationID, id)
					}
					operations[number][activity.OperationID] = true
				}
				s.activities[id] = append(s.activities[id], activity)
				s.entries[pos].Comments = append(s.entries[pos].Comments, comment)
			}
		}
	}
	for i := range s.entries {
		sort.Slice(s.entries[i].Comments, func(a, b int) bool { return s.entries[i].Comments[a].ID < s.entries[i].Comments[b].ID })
	}
	for id := range s.activities {
		sort.Slice(s.activities[id], func(i, j int) bool {
			a, _ := strconv.ParseInt(strings.TrimPrefix(s.activities[id][i].ID, "ghc-"), 10, 64)
			b, _ := strconv.ParseInt(strings.TrimPrefix(s.activities[id][j].ID, "ghc-"), 10, 64)
			return a < b
		})
	}
	sort.Slice(s.entries, func(i, j int) bool { return s.entries[i].Issue.Number < s.entries[j].Issue.Number })
	return s, nil
}

func (c *Client) commentIssueNumber(value string) (int, error) {
	u, err := url.Parse(value)
	prefix := "/repos/" + c.repo + "/issues/"
	if err != nil || u.Scheme != "https" || u.Host != "api.github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !strings.HasPrefix(strings.ToLower(u.Path), strings.ToLower(prefix)) {
		return 0, fmt.Errorf("invalid repository comment issue_url %q", value)
	}
	number := u.Path[len(prefix):]
	n, err := strconv.Atoi(number)
	if err != nil || n <= 0 || strconv.Itoa(n) != number {
		return 0, fmt.Errorf("invalid repository comment issue_url %q", value)
	}
	return n, nil
}

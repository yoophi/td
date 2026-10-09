package ghstore

import (
	"context"
	"fmt"
	"github.com/marcus/td/internal/models"
	"sort"
	"strings"
	"time"
)

// ExtendedStats counts visible issues, but retains logs/handoffs on deleted
// issues like SQLite. It never mixes device-local analytics into shared totals.
// Listing and activity reads are separate observations, not an atomic snapshot.
func (c *Client) ExtendedStats(ctx context.Context, now time.Time) (*models.ExtendedStats, error) {
	records, err := c.ListIncludingDeleted(ctx, true)
	if err != nil {
		return nil, err
	}
	stats, err := aggregateIssueStats(records, now)
	if err != nil {
		return nil, err
	}
	sessions := map[string]int{}
	seen := map[string]bool{}
	for _, record := range records {
		activities, err := c.ListActivityIncludingDeleted(ctx, record.ID)
		if err != nil {
			return nil, fmt.Errorf("read statistics activity on %s: %w", record.ID, err)
		}
		for _, activity := range activities {
			if seen[activity.ID] {
				return nil, fmt.Errorf("statistics activity %s appeared on multiple issues", activity.ID)
			}
			seen[activity.ID] = true
			switch activity.Kind {
			case "log":
				stats.TotalLogs++
				sessions[activity.SessionID]++
			case "handoff":
				stats.TotalHandoffs++
			}
		}
	}
	most := 0
	for session, count := range sessions {
		if count > most || (count == most && session < stats.MostActiveSession) {
			most = count
			stats.MostActiveSession = session
		}
	}
	return stats, nil
}

func aggregateIssueStats(records []Record, now time.Time) (*models.ExtendedStats, error) {
	s := &models.ExtendedStats{ByStatus: map[models.Status]int{}, ByType: map[models.Type]int{}, ByPriority: map[models.Priority]int{}}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	tomorrow := today.AddDate(0, 0, 1)
	weekAgo := now.AddDate(0, 0, -7)
	// Stable issue-number ties keep repeated reads deterministic.
	ordered := append([]Record(nil), records...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Number < ordered[j].Number })
	for _, record := range ordered {
		issue := record.Issue
		if issue.DeletedAt != nil {
			continue
		}
		next := s.TotalPoints + issue.Points
		if issue.Points < 0 || next < s.TotalPoints {
			return nil, fmt.Errorf("statistics points are invalid or overflow on %s", issue.ID)
		}
		s.Total++
		s.TotalPoints = next
		s.ByStatus[issue.Status]++
		s.ByType[issue.Type]++
		s.ByPriority[issue.Priority]++
		if !issue.CreatedAt.Before(today) && issue.CreatedAt.Before(tomorrow) {
			s.CreatedToday++
		}
		if !issue.CreatedAt.Before(weekAgo) {
			s.CreatedThisWeek++
		}
		if issue.Status == models.StatusOpen && (s.OldestOpen == nil || issue.CreatedAt.Before(s.OldestOpen.CreatedAt)) {
			s.OldestOpen = &issue
		}
		if s.NewestTask == nil || !issue.CreatedAt.Before(s.NewestTask.CreatedAt) {
			s.NewestTask = &issue
		}
		if issue.Status == models.StatusClosed {
			if s.LastClosed == nil || (issue.ClosedAt != nil && (s.LastClosed.ClosedAt == nil || !issue.ClosedAt.Before(*s.LastClosed.ClosedAt))) || (issue.ClosedAt == nil && s.LastClosed.ClosedAt == nil) {
				s.LastClosed = &issue
			}
		}
	}
	if s.Total > 0 {
		s.AvgPointsPerTask = float64(s.TotalPoints) / float64(s.Total)
		s.CompletionRate = float64(s.ByStatus[models.StatusClosed]) / float64(s.Total)
	}
	return s, nil
}

// DistinctLabels returns only labels used by non-deleted issues (including
// closed issues), not unused repository definitions. Label commas stay literal.
func (c *Client) DistinctLabels(ctx context.Context) ([]string, error) {
	records, err := c.List(ctx, true)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	labels := []string{}
	for _, record := range records {
		for _, label := range record.Labels {
			label = strings.TrimSpace(label)
			if label != "" && !seen[label] {
				seen[label] = true
				labels = append(labels, label)
			}
		}
	}
	sort.Strings(labels)
	return labels, nil
}

package backendstore

import (
	"context"
	"fmt"
)

type Stats struct {
	Total             int            `json:"total"`
	ByStatus          map[string]int `json:"by_status"`
	ByType            map[string]int `json:"by_type"`
	ByPriority        map[string]int `json:"by_priority"`
	OldestOpen        *Issue         `json:"oldest_open"`
	NewestTask        *Issue         `json:"newest_task"`
	LastClosed        *Issue         `json:"last_closed"`
	CreatedToday      int            `json:"created_today"`
	CreatedThisWeek   int            `json:"created_this_week"`
	TotalPoints       int            `json:"total_points"`
	AvgPointsPerTask  float64        `json:"avg_points_per_task"`
	CompletionRate    float64        `json:"completion_rate"`
	TotalLogs         int            `json:"total_logs"`
	TotalHandoffs     int            `json:"total_handoffs"`
	MostActiveSession string         `json:"most_active_session"`
}

func (c *Connection) Stats(ctx context.Context) (Stats, error) {
	var out Stats
	path, e := scopedPath(c.ProjectID, c.SessionID)
	if e != nil {
		return out, e
	}
	var required struct {
		Stats
		Total *int `json:"total"`
	}
	e = c.Client.request(ctx, "GET", path+"/stats", c.SessionID, "", nil, &required)
	if e != nil {
		return out, e
	}
	if required.Total == nil || *required.Total < 0 || required.ByStatus == nil || required.ByType == nil || required.ByPriority == nil {
		return out, fmt.Errorf("invalid backend statistics response")
	}
	out = required.Stats
	out.Total = *required.Total
	for _, issue := range []*Issue{out.OldestOpen, out.NewestTask, out.LastClosed} {
		if issue != nil {
			if e := validateIssue(*issue); e != nil {
				return Stats{}, e
			}
		}
	}
	return out, nil
}

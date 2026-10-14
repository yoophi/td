package backendstore

import (
	"context"
	"fmt"
)

func (c *Connection) ReviewBuckets(ctx context.Context) (map[string]bool, map[string]bool, error) {
	path, e := scopedPath(c.ProjectID, c.SessionID)
	if e != nil {
		return nil, nil, e
	}
	var out struct {
		SessionID string `json:"session_id"`
		Monitor   struct {
			TaskList struct {
				Reviewable []Issue `json:"reviewable"`
				Ready      []Issue `json:"ready_to_close"`
			} `json:"task_list"`
		} `json:"monitor"`
	}
	if e = c.Client.request(ctx, "GET", path+"/monitor?include_closed=true", c.SessionID, "", nil, &out); e != nil {
		return nil, nil, e
	}
	if out.SessionID != c.SessionID {
		return nil, nil, fmt.Errorf("backend monitor returned a different caller")
	}
	awaiting, ready := map[string]bool{}, map[string]bool{}
	for _, group := range []struct {
		rows []Issue
		ids  map[string]bool
	}{{out.Monitor.TaskList.Reviewable, awaiting}, {out.Monitor.TaskList.Ready, ready}} {
		for _, row := range group.rows {
			if e := validateIssue(row); e != nil {
				return nil, nil, e
			}
			group.ids[row.ID] = true
		}
	}
	return awaiting, ready, nil
}

package backendstore

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"
)

// Monitor is one server-side snapshot. Its token applies to every row in the
// response; callers must discard the whole result when validation fails.
type Monitor struct {
	SessionID   string          `json:"session_id"`
	ChangeToken string          `json:"change_token"`
	Dashboard   MonitorSnapshot `json:"monitor"`
}

type MonitorSnapshot struct {
	ObservationMode  string            `json:"observation_mode"`
	FullReconciledAt string            `json:"full_reconciled_at"`
	FocusedIssue     *Issue            `json:"focused_issue"`
	InProgress       []Issue           `json:"in_progress"`
	Activity         []MonitorActivity `json:"activity"`
	TaskList         MonitorTaskList   `json:"task_list"`
	RecentHandoffs   []MonitorHandoff  `json:"recent_handoffs"`
	ActiveSessions   []string          `json:"active_sessions"`
	Timestamp        time.Time         `json:"timestamp"`
}

type MonitorTaskList struct {
	ReviewVerificationRequired bool    `json:"review_verification_required"`
	Reviewable                 []Issue `json:"reviewable"`
	NeedsRework                []Issue `json:"needs_rework"`
	InProgress                 []Issue `json:"in_progress"`
	Ready                      []Issue `json:"ready"`
	PendingReview              []Issue `json:"pending_review"`
	ReadyToClose               []Issue `json:"ready_to_close"`
	PendingOther               []Issue `json:"pending_other"`
	Blocked                    []Issue `json:"blocked"`
	Closed                     []Issue `json:"closed"`
}

type MonitorActivity struct {
	Timestamp    time.Time `json:"timestamp"`
	SessionID    string    `json:"session_id"`
	Type         string    `json:"type"`
	IssueID      string    `json:"issue_id"`
	IssueTitle   string    `json:"issue_title"`
	Message      string    `json:"message"`
	LogType      string    `json:"log_type"`
	Action       string    `json:"action"`
	EntityID     string    `json:"entity_id"`
	EntityType   string    `json:"entity_type"`
	PreviousData string    `json:"previous_data"`
	NewData      string    `json:"new_data"`
}

type MonitorHandoff struct {
	IssueID   string    `json:"issue_id"`
	SessionID string    `json:"session_id"`
	Timestamp time.Time `json:"timestamp"`
}

func (c *Connection) Monitor(ctx context.Context, search string, includeClosed bool, sort string) (Monitor, error) {
	var out Monitor
	path, err := scopedPath(c.ProjectID, c.SessionID)
	if err != nil {
		return out, err
	}
	values := url.Values{"search_mode": {"auto"}, "include_closed": {strconv.FormatBool(includeClosed)}}
	if search != "" {
		values.Set("search", search)
	}
	if sort != "" {
		values.Set("sort", sort)
	}
	if err := c.Client.request(ctx, "GET", path+"/monitor?"+values.Encode(), c.SessionID, "", nil, &out); err != nil {
		return Monitor{}, err
	}
	if out.SessionID != c.SessionID || out.ChangeToken == "" || out.Dashboard.ObservationMode != "snapshot" || out.Dashboard.Timestamp.IsZero() || out.Dashboard.TaskList.ReviewVerificationRequired {
		return Monitor{}, fmt.Errorf("invalid backend monitor snapshot")
	}
	if out.Dashboard.FocusedIssue != nil {
		if err := ValidateIssue(*out.Dashboard.FocusedIssue); err != nil {
			return Monitor{}, err
		}
	}
	for _, rows := range [][]Issue{out.Dashboard.InProgress, out.Dashboard.TaskList.Reviewable, out.Dashboard.TaskList.NeedsRework, out.Dashboard.TaskList.InProgress, out.Dashboard.TaskList.Ready, out.Dashboard.TaskList.PendingReview, out.Dashboard.TaskList.ReadyToClose, out.Dashboard.TaskList.PendingOther, out.Dashboard.TaskList.Blocked, out.Dashboard.TaskList.Closed} {
		if rows == nil {
			return Monitor{}, fmt.Errorf("incomplete backend monitor snapshot")
		}
		for _, issue := range rows {
			if err := ValidateIssue(issue); err != nil {
				return Monitor{}, err
			}
		}
	}
	if out.Dashboard.Activity == nil || out.Dashboard.RecentHandoffs == nil || out.Dashboard.ActiveSessions == nil {
		return Monitor{}, fmt.Errorf("incomplete backend monitor snapshot")
	}
	return out, nil
}

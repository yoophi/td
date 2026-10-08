package models

import "time"

// Activity is shared issue history. SessionID is an attribution, not proof of
// independent review. The backend supplies ID/Author/CreatedAt/UpdatedAt.
type Activity struct {
	ID            string       `json:"id,omitempty"`
	IssueID       string       `json:"issue_id,omitempty"`
	Kind          string       `json:"kind"`
	OperationID   string       `json:"operation_id,omitempty"`
	SessionID     string       `json:"session_id,omitempty"`
	WorkSessionID string       `json:"work_session_id,omitempty"`
	Message       string       `json:"message,omitempty"`
	LogType       LogType      `json:"log_type,omitempty"`
	Done          []string     `json:"done,omitempty"`
	Remaining     []string     `json:"remaining,omitempty"`
	Decisions     []string     `json:"decisions,omitempty"`
	Uncertain     []string     `json:"uncertain,omitempty"`
	Snapshot      *GitSnapshot `json:"snapshot,omitempty"`
	Author        string       `json:"author,omitempty"`
	CreatedAt     time.Time    `json:"created_at"`
	UpdatedAt     time.Time    `json:"updated_at"`
	URL           string       `json:"url,omitempty"`
	Native        bool         `json:"native,omitempty"`
	Edited        bool         `json:"edited,omitempty"`
}

package models

import (
	"encoding/json"
	"time"
)

// PreservedHistory is passive imported source/backup data, never an active
// workflow event or approval. Data is the original versioned JSON envelope.
type PreservedHistory struct {
	Kind          string          `json:"kind"`
	SourceKey     string          `json:"source_key"`
	SourceDigest  string          `json:"source_digest"`
	ContentDigest string          `json:"content_digest"`
	PartsPresent  int             `json:"parts_present"`
	PartsExpected int             `json:"parts_expected"`
	Complete      bool            `json:"complete"`
	Data          json.RawMessage `json:"data,omitempty"`
}

// Activity is shared issue history. SessionID is an attribution, not proof of
// independent review. The backend supplies ID/Author/CreatedAt/UpdatedAt.
type Activity struct {
	ID               string            `json:"id,omitempty"`
	IssueID          string            `json:"issue_id,omitempty"`
	Kind             string            `json:"kind"`
	OperationID      string            `json:"operation_id,omitempty"`
	SessionID        string            `json:"session_id,omitempty"`
	WorkSessionID    string            `json:"work_session_id,omitempty"`
	Message          string            `json:"message,omitempty"`
	LogType          LogType           `json:"log_type,omitempty"`
	Done             []string          `json:"done,omitempty"`
	Remaining        []string          `json:"remaining,omitempty"`
	Decisions        []string          `json:"decisions,omitempty"`
	Uncertain        []string          `json:"uncertain,omitempty"`
	Snapshot         *GitSnapshot      `json:"snapshot,omitempty"`
	Author           string            `json:"author,omitempty"`
	CreatedAt        time.Time         `json:"created_at"`
	UpdatedAt        time.Time         `json:"updated_at"`
	URL              string            `json:"url,omitempty"`
	Native           bool              `json:"native,omitempty"`
	Edited           bool              `json:"edited,omitempty"`
	PreservedHistory *PreservedHistory `json:"preserved_history,omitempty"`
}

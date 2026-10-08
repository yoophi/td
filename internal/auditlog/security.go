package auditlog

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

const SecurityEventsFile = ".todos/security_events.jsonl"

// SecurityEvent represents a security-relevant workflow exception
// (for example, self-close or creator-approval exceptions).
type SecurityEvent struct {
	Repository  string    `json:"repository,omitempty"`
	OperationID string    `json:"operation_id,omitempty"`
	Outcome     string    `json:"outcome,omitempty"`
	Scope       string    `json:"scope,omitempty"`
	Timestamp   time.Time `json:"ts"`
	IssueID     string    `json:"issue_id"`
	SessionID   string    `json:"session_id"`
	AgentType   string    `json:"agent_type,omitempty"`
	Reason      string    `json:"reason"`
}

// LogSecurityEvent appends a security event to the jsonl file
func LogSecurityEvent(baseDir string, event SecurityEvent) error {
	errPath := filepath.Join(baseDir, SecurityEventsFile)

	// Ensure .todos directory exists
	todosDir := filepath.Dir(errPath)
	if _, err := os.Stat(todosDir); os.IsNotExist(err) {
		if err := os.MkdirAll(todosDir, 0755); err != nil {
			return err
		}
	}

	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}

	data, err := json.Marshal(event)
	if err != nil {
		return err
	}

	f, err := os.OpenFile(errPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	line := append(data, '\n')
	n, err := f.Write(line)
	if err == nil && n != len(line) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

// ReadSecurityEvents reads all security events from the file
func ReadSecurityEvents(baseDir string) ([]SecurityEvent, error) {
	errPath := filepath.Join(baseDir, SecurityEventsFile)

	data, err := os.ReadFile(errPath)
	if os.IsNotExist(err) {
		return []SecurityEvent{}, nil
	}
	if err != nil {
		return nil, err
	}

	var events []SecurityEvent
	start := 0
	lineNumber := 0
	for i := 0; i <= len(data); i++ {
		if i == len(data) || data[i] == '\n' {
			lineNumber++
			if i > start {
				line := data[start:i]
				var e SecurityEvent
				if err := json.Unmarshal(line, &e); err != nil {
					return nil, fmt.Errorf("invalid security audit JSONL at line %d: %w", lineNumber, err)
				}
				events = append(events, e)
			}
			start = i + 1
		}
	}

	return events, nil
}

// ClearSecurityEvents removes the security events file
func ClearSecurityEvents(baseDir string) error {
	errPath := filepath.Join(baseDir, SecurityEventsFile)
	err := os.Remove(errPath)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

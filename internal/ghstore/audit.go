package ghstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/marcus/td/internal/auditlog"
)

// The local audit is separate from editable shared GitHub review metadata.
// Record intent before sending the write, then its verified or uncertain result.
// A local failure after a confirmed remote write must not suggest rollback.
func (c *Client) auditedUpdate(ctx context.Context, observed *Record, change Changes, options TransitionOptions, operation, reason string) (*Record, error) {
	if reason == "" || c.audit == nil {
		return c.UpdateObserved(ctx, observed, change)
	}
	event := auditlog.SecurityEvent{IssueID: observed.ID, SessionID: options.SessionID, AgentType: options.AgentType, Repository: c.repo, OperationID: operation, Reason: reason, Scope: "device-local", Outcome: "attempted"}
	if err := c.audit(event); err != nil {
		return nil, fmt.Errorf("%s audit intent could not be saved; no issue transition write attempted: %w", observed.ID, err)
	}
	result, err := c.UpdateObserved(ctx, observed, change)
	if err != nil {
		event.Outcome = "uncertain"
		if auditErr := c.audit(event); auditErr != nil {
			return nil, errors.Join(err, fmt.Errorf("operation %s also failed to record its uncertain audit outcome: %w", operation, auditErr))
		}
		return nil, err
	}
	event.Outcome = "confirmed"
	if err := c.audit(event); err != nil {
		return nil, fmt.Errorf("%s is saved as %s on GitHub (operation %s), but its confirmed audit outcome could not be saved; earlier changes remain, inspect current state before retrying: %w", result.ID, result.Status, operation, err)
	}
	return result, nil
}

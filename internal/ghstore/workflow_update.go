package ghstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/workflow"
)

func (c Changes) HasFields() bool {
	return c.ParentID != nil || c.Title != nil || c.Description != nil || c.Acceptance != nil || c.Type != nil || c.Priority != nil || c.Points != nil || c.Labels != nil || c.Details != nil || c.Minor != nil || c.Sprint != nil || c.Reason != nil
}

// UpdateWorkflow applies edits before evaluating the requested transition, so
// edits cannot ride on an approval of older content. These are separate writes;
// errors identify already-saved edits and never retry an uncertain operation.
func (c *Client) UpdateWorkflow(ctx context.Context, id string, change Changes, options TransitionOptions) (*Record, error) {
	if change.Status != nil && strings.TrimSpace(options.SessionID) == "" {
		return nil, fmt.Errorf("status update requires a session")
	}
	observed, err := c.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return c.updateWorkflowObserved(ctx, observed, change, options)
}

// UpdateWorkflowObserved retains the caller's original observation across field
// edits and the subsequent workflow/cascade write. Read-back verifies that exact
// revision; it never accepts a newer root in place of a stale form observation.
// Separate field/status requests are not an atomic transaction.
func (c *Client) UpdateWorkflowObserved(ctx context.Context, observed *Record, change Changes, options TransitionOptions) (*Record, error) {
	if observed == nil || observed.repository != c.repo || observed.revision == ([32]byte{}) {
		return nil, fmt.Errorf("workflow update requires an observation from this repository")
	}
	if n, err := Number(observed.ID); err != nil || n != observed.Number {
		return nil, fmt.Errorf("observed issue identity is inconsistent")
	}
	if change.Status != nil && strings.TrimSpace(options.SessionID) == "" {
		return nil, fmt.Errorf("status update requires a session")
	}
	current, err := c.Get(ctx, observed.ID)
	if err != nil {
		return nil, err
	}
	if current.revision != observed.revision {
		return nil, &ConflictError{ID: observed.ID}
	}
	return c.updateWorkflowObserved(ctx, current, change, options)
}

func (c *Client) updateWorkflowObserved(ctx context.Context, observed *Record, change Changes, options TransitionOptions) (*Record, error) {
	if change.Status == nil {
		return c.UpdateObserved(ctx, observed, change)
	}
	action, err := workflowUpdateAction(observed.Status, *change.Status)
	if err != nil {
		return nil, err
	}
	if action == "review" || action == "approve" || action == "reject" || action == "close" || action == "reopen" {
		if err := ValidateReviewOptions(action, options); err != nil {
			return nil, err
		}
	}
	change.Status = nil
	edited := change.HasFields()
	written := observed
	if edited {
		written, err = c.UpdateObserved(ctx, observed, change)
		if err != nil {
			return nil, err
		}
	}
	record, _, err := c.TransitionObservedWithCascades(ctx, written, action, options)
	if err != nil && edited {
		return nil, fmt.Errorf("%s field changes were saved, but requested %s transition failed; inspect current status and do not repeat the entire update: %w", observed.ID, action, err)
	}
	return record, err
}

func workflowUpdateAction(from, to models.Status) (string, error) {
	if from != to && !workflow.DefaultMachine().IsValidTransition(from, to) {
		return "", fmt.Errorf("invalid status transition from %s to %s", from, to)
	}
	switch to {
	case models.StatusInProgress:
		return "start", nil
	case models.StatusBlocked:
		return "block", nil
	case models.StatusInReview:
		return "review", nil
	case models.StatusClosed:
		if from == models.StatusInReview {
			return "approve", nil
		}
		return "close", nil
	case models.StatusOpen:
		switch from {
		case models.StatusClosed:
			return "reopen", nil
		case models.StatusInReview:
			return "reject", nil
		case models.StatusBlocked:
			return "unblock", nil
		default:
			return "unstart", nil
		}
	default:
		return "", fmt.Errorf("invalid status %q", to)
	}
}

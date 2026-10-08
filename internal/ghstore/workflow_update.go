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
	if change.Status == nil {
		return c.Update(ctx, id, change)
	}
	if strings.TrimSpace(options.SessionID) == "" {
		return nil, fmt.Errorf("status update requires a session")
	}
	observed, err := c.Get(ctx, id)
	if err != nil {
		return nil, err
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
	if edited {
		if _, err := c.UpdateObserved(ctx, observed, change); err != nil {
			return nil, err
		}
	}
	record, _, err := c.TransitionWithCascades(ctx, id, action, options)
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

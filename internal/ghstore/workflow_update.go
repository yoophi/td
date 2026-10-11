package ghstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/workflow"
)

func (c Changes) HasFields() bool {
	return c.DueDate != nil || c.DeferUntil != nil || c.ParentID != nil || c.Title != nil || c.Description != nil || c.Acceptance != nil || c.Type != nil || c.Priority != nil || c.Points != nil || c.Labels != nil || c.Details != nil || c.Minor != nil || c.Sprint != nil || c.Reason != nil
}

// UpdateWorkflow applies edits before evaluating the requested transition, so
// edits cannot ride on an approval of older content. Tracked calls keep these
// separate writes in one command journal; errors identify already-saved edits
// and never retry an uncertain operation.
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
	if change.Status != nil && c.journal != nil && c.journal.Group == nil {
		return c.UpdateCommandObserved(ctx, observed, change, RelationChanges{}, options)
	}
	if change.Status == nil {
		return c.UpdateObserved(ctx, observed, change)
	}
	action, err := workflowUpdateRequest(observed.Status, *change.Status, options)
	if err != nil {
		return nil, err
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

// UpdateCommandObserved is the single save boundary for observed edit forms.
// An omitted status preserves the existing workflow, including review requests.
// Every requested dependency and field is validated before the first task write.
func (c *Client) UpdateCommandObserved(ctx context.Context, observed *Record, change Changes, relations RelationChanges, options TransitionOptions) (*Record, error) {
	if observed == nil || observed.repository != c.repo || observed.revision == ([32]byte{}) {
		return nil, fmt.Errorf("command update requires an observation from this repository")
	}
	if n, err := Number(observed.ID); err != nil || n != observed.Number {
		return nil, fmt.Errorf("observed issue identity is inconsistent")
	}
	if c.journal == nil || c.journal.SessionID != options.SessionID {
		return nil, fmt.Errorf("command update requires its tracked actor; no write attempted")
	}
	var records []Record
	var err error
	if change.Status != nil {
		records, err = c.updateWorkflowCommand(ctx, []string{observed.ID}, change, relations, options, nil, observed)
	} else {
		records, err = c.updateWithRelations(ctx, []string{observed.ID}, change, relations, nil, observed)
	}
	if err != nil {
		return nil, err
	}
	if len(records) != 1 || records[0].ID != observed.ID {
		return nil, fmt.Errorf("command result is unverified; inspect GitHub before retrying")
	}
	return &records[0], nil
}

func (c *Client) checkCommandObservation(records map[string]Record, observed *Record) error {
	current, found := records[observed.ID]
	if !found || observed.repository != c.repo || observed.Number != current.Number || observed.revision == ([32]byte{}) || current.revision != observed.revision {
		return &ConflictError{ID: observed.ID}
	}
	return nil
}

func workflowUpdateRequest(from, to models.Status, options TransitionOptions) (string, error) {
	if strings.TrimSpace(options.SessionID) == "" {
		return "", fmt.Errorf("status update requires a session")
	}
	action, err := workflowUpdateAction(from, to)
	if err != nil {
		return "", err
	}
	if action == "review" || action == "approve" || action == "reject" || action == "close" || action == "reopen" {
		if err := ValidateReviewOptions(action, options); err != nil {
			return "", err
		}
	}
	return action, nil
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

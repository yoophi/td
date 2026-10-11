package ghstore

import (
	"context"
	"crypto/rand"
	"fmt"

	"github.com/marcus/td/internal/models"
)

// PreflightUpdate checks selected tasks, field payloads, transition requests and
// comment publication size without writing labels, tasks, comments or journals.
// This is input validation, not a persisted mixed-effect plan. Callers must still
// validate relationships and run policy/cascade and freshness checks at execution.
func (c *Client) PreflightUpdate(ctx context.Context, ids []string, change Changes, options TransitionOptions, activity *models.Activity) ([]Record, error) {
	if len(ids) == 0 {
		return nil, fmt.Errorf("update requires at least one task; no write attempted")
	}
	var records []Record
	if len(ids) == 1 {
		r, err := c.Get(ctx, ids[0])
		if err != nil {
			return nil, err
		}
		records = []Record{*r}
	} else {
		var err error
		records, err = c.selectPlannedTasks(ctx, ids, false)
		if err != nil {
			return nil, err
		}
	}
	fields := change
	fields.Status = nil
	for i := range records {
		r := &records[i]
		if change.Status != nil {
			action, err := workflowUpdateRequest(r.Status, *change.Status, options)
			if err == nil {
				switch action {
				case "start", "unstart", "block", "unblock":
					_, _, err = basicTransitionDecision(r, action, options)
				}
			}
			if err != nil {
				return nil, fmt.Errorf("update preflight %s (no write attempted): %w", r.ID, err)
			}
		}
		if fields.HasFields() {
			if _, err := c.prepareIssueUpdate(ctx, r, fields); err != nil {
				return nil, fmt.Errorf("update preflight %s (no write attempted): %w", r.ID, err)
			}
		}
		if activity != nil {
			input := *activity
			if input.OperationID == "" {
				// Publication allocates its own ID of the same length. This value
				// is used only for validation and sizing, never persisted.
				input.OperationID = "td-op-" + rand.Text()
			}
			if _, _, err := prepareActivityInput(r.ID, input); err != nil {
				return nil, fmt.Errorf("update comment preflight %s (no write attempted): %w", r.ID, err)
			}
		}
	}
	return records, nil
}

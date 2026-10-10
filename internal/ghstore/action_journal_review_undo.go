package ghstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/marcus/td/internal/models"
)

// Validate review invalidation without discarding or reattributing old rows.
func revokedReviewDetails(bd, ad IssueDetails) (IssueDetails, bool) {
	expected := bd
	expected.ReviewerSession, expected.ReviewBasis = "", ""
	expected.ReviewedAt = nil
	if len(bd.Reviews) != len(ad.Reviews) {
		return IssueDetails{}, false
	}
	var revokedAt *time.Time
	for i := range bd.Reviews {
		b, a := bd.Reviews[i], ad.Reviews[i]
		if b.SupersededAt == nil {
			if a.SupersededAt == nil || a.SupersededAt.Before(b.CreatedAt) || revokedAt != nil && !revokedAt.Equal(*a.SupersededAt) {
				return IssueDetails{}, false
			}
			revokedAt = a.SupersededAt
			b.SupersededAt = a.SupersededAt
		}
		if !reflect.DeepEqual(b, a) {
			return IssueDetails{}, false
		}
	}
	expected.Reviews = ad.Reviews
	return expected, true
}

// Restoring a previously valid review is not a new approval. The original
// handoff and native event basis must still be valid at every write fence.
func (c *Client) checkUndoOriginalReview(ctx context.Context, action *ActionJournalRecord, afterWrite bool) error {
	ctx = withReviewCost(ctx)
	fileChange, err := fileUndoScope(action)
	if err != nil {
		return err
	}
	if !fileChange {
		relationChange, err := relationUndoScope(action)
		if err != nil {
			return err
		}
		if !relationChange {
			workflowChange, err := workflowUndoScope(action)
			if err != nil {
				return err
			}
			if !workflowChange {
				reviewChange, err := reviewTransitionUndoScope(action)
				if err != nil || !reviewChange {
					return err
				}
			}
		}
	}
	_, meta, _, err := decodeBody(action.Before.Body)
	if err != nil || meta.Details == nil {
		return err
	}
	d := *meta.Details
	if d.ReviewBasis == "" && d.ReviewerSession == "" && d.ReviewedAt == nil && activeApproval(&d) == nil {
		return nil
	}
	n, err := Number(action.IssueID)
	if err != nil {
		return err
	}
	native := apiIssue{Number: n, Title: action.Before.Title, Body: action.Before.Body, State: action.Before.State, UpdatedAt: action.Before.UpdatedAt}
	data, err := json.Marshal(native)
	if err != nil {
		return err
	}
	original, err := decodeIssue(data)
	if err != nil {
		return err
	}
	if original.Status == models.StatusClosed {
		err = c.verifyClosedApproval(ctx, original, d)
		if err == nil {
			err = c.verifyReviewHandoff(ctx, original, d)
		}
	} else {
		_, err = c.verifyReview(ctx, original, d)
	}
	if err != nil {
		var stale *WorkflowStateError
		if errors.As(err, &stale) {
			return fmt.Errorf("undo cannot restore the original review (%v): %w", err, &ConflictError{ID: action.IssueID, AfterWrite: afterWrite})
		}
		return fmt.Errorf("undo original review verification failed; result must be inspected if target was written (after_write=%t): %w", afterWrite, err)
	}
	return nil
}

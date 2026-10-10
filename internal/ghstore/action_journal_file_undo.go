package ghstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/marcus/td/internal/models"
)

// Recognize the complete single-target file writer effect, including review
// invalidation. Other workflow/graph changes cannot hitch a ride on this inverse.
func fileUndoScope(action *ActionJournalRecord) (bool, error) {
	if action.Action != "update" || action.Before == nil || action.After == nil || action.entityKind() != "" {
		return false, nil
	}
	bdesc, bm, _, err := decodeBody(action.Before.Body)
	if err != nil {
		return false, err
	}
	adesc, am, _, err := decodeBody(action.After.Body)
	if err != nil {
		return false, err
	}
	bd, ad := IssueDetails{}, IssueDetails{}
	if bm.Details != nil {
		bd = *bm.Details
	}
	if am.Details != nil {
		ad = *am.Details
	}
	if bd.DeletedAt != nil || ad.DeletedAt != nil {
		return false, nil
	}
	if bd.Status == "" {
		bd.Status = models.Status(action.Before.State)
	}
	if ad.Status == "" {
		ad.Status = models.Status(action.After.State)
	}
	if reflect.DeepEqual(bd.Files, ad.Files) || len(bd.Files) == 0 && len(ad.Files) == 0 {
		return false, nil
	}
	if _, err := validatedLinkedFiles(action.IssueID, bd.Files); err != nil {
		return false, err
	}
	if _, err := validatedLinkedFiles(action.IssueID, ad.Files); err != nil {
		return false, err
	}
	bm.Details, am.Details = nil, nil
	bl := mirroredPriorityLabels(mirroredLabels(action.Before.Labels, ad.Status), am.Priority)
	al := slices.Clone(action.After.Labels)
	slices.Sort(bl)
	slices.Sort(al)
	if !reflect.DeepEqual(bm, am) || bdesc != adesc || action.Before.Title != action.After.Title || action.Before.State != action.After.State || !slices.Equal(bl, al) {
		return false, nil
	}
	// A normal file edit clears only the current review pointers and supersedes
	// every active review at one instant, retaining all original review rows.
	expected := bd
	expected.Files = ad.Files
	expected.ReviewerSession, expected.ReviewBasis = "", ""
	expected.ReviewedAt = nil
	if len(bd.Reviews) != len(ad.Reviews) {
		return false, nil
	}
	var revokedAt *time.Time
	for i := range bd.Reviews {
		b, a := bd.Reviews[i], ad.Reviews[i]
		if b.SupersededAt == nil {
			if a.SupersededAt == nil || a.SupersededAt.Before(b.CreatedAt) || revokedAt != nil && !revokedAt.Equal(*a.SupersededAt) {
				return false, nil
			}
			revokedAt = a.SupersededAt
			b.SupersededAt = a.SupersededAt
		}
		if !reflect.DeepEqual(b, a) {
			return false, nil
		}
	}
	expected.Reviews = ad.Reviews
	return reflect.DeepEqual(expected, ad), nil
}

// Restoring a previously valid review is not a new approval. The original
// handoff and native event basis must still be valid at every write fence.
func (c *Client) checkFileUndoReview(ctx context.Context, action *ActionJournalRecord, afterWrite bool) error {
	ctx = withReviewCost(ctx)
	fileChange, err := fileUndoScope(action)
	if err != nil || !fileChange {
		return err
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
			return fmt.Errorf("file undo cannot restore the original review (%v): %w", err, &ConflictError{ID: action.IssueID, AfterWrite: afterWrite})
		}
		return fmt.Errorf("file undo review verification failed; result must be inspected if target was written (after_write=%t): %w", afterWrite, err)
	}
	return nil
}

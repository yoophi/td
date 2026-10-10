package ghstore

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Deletion retains the original comment's identity, author, clocks and raw body.
// GitHub cannot recreate that identity: this receipt records an irreversible
// native deletion rather than claiming that posting a copy would undo it.
func (c *Client) prepareCommentDeletionJournal(ctx context.Context, issue *Record, comment apiComment) (*ActionJournalRecord, error) {
	before, err := actionIssueState(issue)
	if err != nil {
		return nil, err
	}
	original, err := c.nativeCommentState(comment, issue.ID, comment.Body)
	if err != nil {
		return nil, err
	}
	intent := ActionJournal{OperationID: "td-op-" + rand.Text(), SessionID: c.journal.SessionID,
		IssueID: issue.ID, CarrierID: "gh-1", Action: "comment_delete", Phase: "prepared",
		Before: &before, CommentBefore: original}
	if issue.ID == intent.CarrierID {
		intent.CarrierID = "gh-2"
	}
	if err := intent.validate(); err != nil {
		return nil, err
	}
	if err := c.EnableActionJournal(ctx, c.journal.SessionID); err != nil {
		return nil, err
	}
	intent.CarrierID = c.journal.Carrier.ID
	return c.writePreparedJournal(ctx, intent)
}

// GitHub may publish the DELETE clock to collection/point reads at different
// times. Reobserve only that exact clock-only difference before committing the
// receipt. Never replay DELETE or relax the final receipt's revision check.
type commentDeletionClockLagError struct{ cause error }

func (e *commentDeletionClockLagError) Error() string { return e.cause.Error() }
func (e *commentDeletionClockLagError) Unwrap() error { return e.cause }

func (c *Client) observeCommentDeletion(ctx context.Context, prepared *ActionJournalRecord) (*Record, error) {
	for attempt := 0; attempt < 3; attempt++ {
		after, err := c.GetIncludingDeleted(withReadbackCost(ctx), prepared.IssueID)
		if err != nil {
			return nil, err
		}
		state, err := actionIssueState(after)
		if err != nil {
			return nil, err
		}
		if !sameActionContents(*prepared.Before, state) || state.UpdatedAt.Before(prepared.Before.UpdatedAt) {
			return nil, &ConflictError{ID: prepared.IssueID, AfterWrite: true}
		}
		err = c.verifyCommentDeletionJournal(ctx, prepared, after)
		if err == nil {
			// A collection match can precede a delayed point-read clock update.
			// Fence it once more before publishing an immutable applied state.
			err = c.verifyCommentDeletionPoint(ctx, after)
			if err == nil {
				return after, nil
			}
		}
		var lag *commentDeletionClockLagError
		if !errors.As(err, &lag) || attempt == 2 {
			return nil, err
		}
		timer := time.NewTimer(time.Duration(attempt+1) * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, fmt.Errorf("comment deletion observation did not converge")
}

func (c *Client) completeCommentDeletionJournal(ctx context.Context, prepared *ActionJournalRecord) error {
	after, err := c.observeCommentDeletion(ctx, prepared)
	if err != nil {
		return err
	}
	if err := c.completeActionJournal(ctx, prepared, after); err != nil {
		return err
	}
	return c.verifyCommentDeletionJournal(ctx, prepared, after)
}

func (c *Client) verifyCommentDeletionJournal(ctx context.Context, prepared *ActionJournalRecord, expected *Record) error {
	snapshot, err := c.ReadSnapshot(WithFreshSnapshot(ctx), true)
	if err != nil {
		return err
	}
	if _, err := snapshot.ActionHistory(""); err != nil {
		return err
	}
	number, err := CommentNumber(prepared.CommentBefore.ID)
	if err != nil {
		return err
	}
	found := false
	for _, entry := range snapshot.entries {
		for _, comment := range entry.Comments {
			if comment.ID == number {
				return fmt.Errorf("native comment %s remains visible; deletion is unverified", prepared.CommentBefore.ID)
			}
		}
		if entry.Issue.Number != expected.Number {
			continue
		}
		found = true
		if err := compareCommentDeletionObservation(entry.Issue, expected, "fresh collection"); err != nil {
			return err
		}
	}
	if !found {
		return fmt.Errorf("target %s missing from fresh collection; comment deletion is unverified", expected.ID)
	}
	return nil
}

func (c *Client) verifyCommentDeletionPoint(ctx context.Context, expected *Record) error {
	data, err := c.request(withReadbackCost(ctx), "GET", fmt.Sprintf("/issues/%d", expected.Number), nil, false)
	if err != nil {
		return err
	}
	var item apiIssue
	if err := json.Unmarshal(data, &item); err != nil {
		return err
	}
	return compareCommentDeletionObservation(item, expected, "post-collection point")
}

func compareCommentDeletionObservation(item apiIssue, expected *Record, source string) error {
	current, err := item.record()
	if err != nil {
		return err
	}
	if current.revision == expected.revision {
		return nil
	}
	cause := fmt.Errorf("%s differs from post-delete point observation (expected updated_at=%s, observed updated_at=%s): %w", source, expected.UpdatedAt.UTC().Format(time.RFC3339Nano), current.UpdatedAt.UTC().Format(time.RFC3339Nano), &ConflictError{ID: expected.ID, AfterWrite: true})
	clockNormalized := item
	clockNormalized.UpdatedAt = expected.UpdatedAt
	normalized, err := clockNormalized.record()
	if err == nil && normalized.revision == expected.revision {
		return &commentDeletionClockLagError{cause: cause}
	}
	return cause
}

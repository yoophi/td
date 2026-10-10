package ghstore

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
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

func (c *Client) completeCommentDeletionJournal(ctx context.Context, prepared *ActionJournalRecord) error {
	after, err := c.GetIncludingDeleted(withReadbackCost(ctx), prepared.IssueID)
	if err != nil {
		return err
	}
	state, err := actionIssueState(after)
	if err != nil {
		return err
	}
	if !sameActionContents(*prepared.Before, state) || state.UpdatedAt.Before(prepared.Before.UpdatedAt) {
		return &ConflictError{ID: prepared.IssueID, AfterWrite: true}
	}
	if err := c.verifyCommentDeletionJournal(ctx, prepared, after); err != nil {
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
		data, err := json.Marshal(entry.Issue)
		if err != nil {
			return err
		}
		current, err := decodeIssue(data)
		if err != nil {
			return err
		}
		if current.revision != expected.revision {
			return &ConflictError{ID: expected.ID, AfterWrite: true}
		}
	}
	if !found {
		return fmt.Errorf("target %s missing from fresh collection; comment deletion is unverified", expected.ID)
	}
	return nil
}

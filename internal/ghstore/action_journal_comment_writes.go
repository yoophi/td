package ghstore

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/marcus/td/internal/models"
)

func (c *Client) nativeCommentState(comment apiComment, issueID, body string) (*ActionCommentState, error) {
	number, err := c.commentIssueNumber(comment.IssueURL)
	if err != nil || fmt.Sprintf("gh-%d", number) != issueID || comment.Body != body {
		return nil, fmt.Errorf("native comment membership or body is unverified")
	}
	state := &ActionCommentState{ID: fmt.Sprintf("ghc-%d", comment.ID), IssueID: issueID,
		Body: comment.Body, Author: comment.User.Login, CreatedAt: comment.CreatedAt, UpdatedAt: comment.UpdatedAt}
	state.Revision, err = state.checksum()
	if err == nil {
		err = state.validate()
	}
	return state, err
}

func (c *Client) readNativeComment(ctx context.Context, expected *ActionCommentState) error {
	number, err := CommentNumber(expected.ID)
	if err != nil {
		return err
	}
	data, err := c.request(ctx, "GET", fmt.Sprintf("/issues/comments/%d", number), nil, false)
	if err != nil {
		return err
	}
	var comment apiComment
	if err := json.Unmarshal(data, &comment); err != nil {
		return fmt.Errorf("native comment readback unreadable: %w", err)
	}
	actual, err := c.nativeCommentState(comment, expected.IssueID, expected.Body)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(actual, expected) {
		return fmt.Errorf("native comment changed before verification")
	}
	return nil
}

// Activity operation identity is unique on its target, while journal operation
// identity is repository-wide. Work-session fan-out can share the former.
func (c *Client) verifyCommentOperation(ctx context.Context, issueID, operationID string, expected *ActionCommentState) error {
	snapshot, err := c.ReadSnapshot(WithFreshSnapshot(ctx), true)
	if err != nil {
		return err
	}
	if _, err := snapshot.ActionHistory(""); err != nil {
		return err
	}
	matches := 0
	for _, entry := range snapshot.entries {
		if fmt.Sprintf("gh-%d", entry.Issue.Number) != issueID {
			continue
		}
		for _, comment := range entry.Comments {
			activity, err := decodeActivity(comment, issueID)
			if err != nil {
				return err
			}
			if activity.Kind == "action_journal" || activity.OperationID != operationID {
				continue
			}
			matches++
			if expected != nil {
				actual, err := c.nativeCommentState(comment, issueID, expected.Body)
				if err != nil || !reflect.DeepEqual(actual, expected) {
					return fmt.Errorf("activity operation %s has a changed or duplicate native result", operationID)
				}
			}
		}
	}
	want := 0
	if expected != nil {
		want = 1
	}
	if matches != want {
		return fmt.Errorf("activity operation %s has %d observed results on %s; inspect before retrying", operationID, matches, issueID)
	}
	return nil
}

type journaledActivityResult struct {
	Activity *models.Activity
	Issue    *Record
	Comment  *ActionCommentState
}

func (c *Client) appendJournaledActivity(ctx context.Context, issue *Record, data activityData, body string) (*models.Activity, error) {
	result, err := c.appendJournaledActivityResult(ctx, issue, data, body)
	if err != nil {
		return nil, err
	}
	return result.Activity, nil
}

// Retain the actual verified issue/comment observations for a following effect.
// A caller must not replace this revision with a fresh unrelated observation.
func (c *Client) appendJournaledActivityResult(ctx context.Context, issue *Record, data activityData, body string) (*journaledActivityResult, error) {
	if data.SessionID != c.journal.SessionID {
		return nil, fmt.Errorf("activity session differs from journal scope; no comment write attempted")
	}
	before, err := actionIssueState(issue)
	if err != nil {
		return nil, err
	}
	intent := ActionJournal{OperationID: "td-op-" + rand.Text(), SessionID: c.journal.SessionID,
		IssueID: issue.ID, CarrierID: "gh-1", Action: "comment_create", Phase: "prepared", Before: &before,
		CommentInput: &ActionCommentInput{Body: body, OperationID: data.OperationID}}
	if issue.ID == intent.CarrierID {
		intent.CarrierID = "gh-2"
	}
	if err := c.bindActivityGroupIntent(&intent); err != nil {
		return nil, err
	}
	// Validate before bootstrap so invalid activity cannot create a carrier.
	if err := intent.validate(); err != nil {
		return nil, err
	}
	if err := c.EnableActionJournal(ctx, c.journal.SessionID); err != nil {
		return nil, err
	}
	intent.CarrierID = c.journal.Carrier.ID
	if err := c.verifyCommentOperation(ctx, issue.ID, data.OperationID, nil); err != nil {
		return nil, fmt.Errorf("verify activity operation before intent (no comment POST attempted): %w", err)
	}
	prepared, err := c.writePreparedJournal(ctx, intent)
	if err != nil {
		return nil, err
	}
	latest, err := c.Get(ctx, issue.ID)
	if err != nil {
		return nil, err
	}
	if latest.revision != issue.revision {
		return nil, &ConflictError{ID: issue.ID}
	}
	if err := c.verifyPlannedUpdateGroup(ctx, nil); err != nil {
		return nil, err
	}
	uncertain := func(err error) (*journaledActivityResult, error) {
		return nil, fmt.Errorf("activity outcome for %s is unverified; inspect target %s and journal %s before retrying: %w", intent.OperationID, issue.ID, prepared.ID, err)
	}
	response, err := c.request(ctx, "POST", fmt.Sprintf("/issues/%d/comments", issue.Number), map[string]string{"body": body}, false)
	if err != nil {
		return uncertain(err)
	}
	var comment apiComment
	if err := json.Unmarshal(response, &comment); err != nil {
		return uncertain(err)
	}
	state, err := c.nativeCommentState(comment, issue.ID, body)
	if err != nil {
		return uncertain(err)
	}
	if !strings.EqualFold(state.Author, prepared.Author) {
		return uncertain(fmt.Errorf("native activity author differs from intent author"))
	}
	if err := c.readNativeComment(withReadbackCost(ctx), state); err != nil {
		return uncertain(err)
	}
	after, err := c.GetIncludingDeleted(withReadbackCost(ctx), issue.ID)
	if err != nil {
		return uncertain(err)
	}
	afterState, err := actionIssueState(after)
	if err != nil {
		return uncertain(err)
	}
	if !sameActionContents(before, afterState) || afterState.UpdatedAt.Before(before.UpdatedAt) {
		return uncertain(&ConflictError{ID: issue.ID, AfterWrite: true})
	}
	if err := c.verifyCommentOperation(ctx, issue.ID, data.OperationID, state); err != nil {
		return uncertain(err)
	}
	if err := c.completeActionJournalResult(ctx, prepared, after, state); err != nil {
		return uncertain(err)
	}
	if err := c.readNativeComment(withReadbackCost(ctx), state); err != nil {
		return uncertain(err)
	}
	result, err := decodeActivity(comment, issue.ID)
	if err != nil {
		return uncertain(err)
	}
	return &journaledActivityResult{Activity: &result, Issue: after, Comment: state}, nil
}

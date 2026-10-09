package ghstore

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

type CommentNotFoundError struct{ CommentID, IssueID string }

func (e *CommentNotFoundError) Error() string {
	return fmt.Sprintf("comment %s not found on issue %s", e.CommentID, e.IssueID)
}

// CommentNumber accepts only repository-local GitHub comment IDs returned by td.
func CommentNumber(id string) (int64, error) {
	raw := strings.TrimPrefix(id, "ghc-")
	for _, c := range raw {
		if c < '0' || c > '9' {
			return 0, &WorkflowInputError{Reason: "invalid GitHub comment ID (use ghc-123 or 123)"}
		}
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return 0, &WorkflowInputError{Reason: "invalid GitHub comment ID (use ghc-123 or 123)"}
	}
	return n, nil
}

// DeleteComment protects td logs/handoffs and validates issue membership before
// deleting a GitHub comment. Revision checks cannot make GitHub DELETE atomic.
// Uncertain outcomes are returned for inspection, never automatically retried.
func (c *Client) DeleteComment(ctx context.Context, issueID, commentID string) error {
	n, err := CommentNumber(commentID)
	if err != nil {
		return err
	}
	canonical := fmt.Sprintf("ghc-%d", n)
	issue, err := c.Get(ctx, issueID)
	if err != nil {
		return err
	}
	activities, err := c.ListActivity(ctx, issue.ID)
	if err != nil {
		return err
	}
	found := false
	for _, a := range activities {
		if a.ID == canonical {
			if a.Kind != "comment" {
				return &PolicyError{Reason: "comment deletion cannot remove td logs or handoffs"}
			}
			found = true
			break
		}
	}
	if !found {
		return &CommentNotFoundError{CommentID: canonical, IssueID: issue.ID}
	}
	path := fmt.Sprintf("/issues/comments/%d", n)
	first, err := c.request(ctx, "GET", path, nil, false)
	if err != nil {
		return err
	}
	var before apiComment
	if err := json.Unmarshal(first, &before); err != nil {
		return fmt.Errorf("invalid GitHub comment response: %w", err)
	}
	if before.ID != n || !strings.EqualFold(before.IssueURL, fmt.Sprintf("https://api.github.com/repos/%s/issues/%d", c.repo, issue.Number)) {
		return &CommentNotFoundError{CommentID: canonical, IssueID: issue.ID}
	}
	activity, err := decodeActivity(before, issue.ID)
	if err != nil {
		return err
	}
	if activity.Kind != "comment" {
		return &PolicyError{Reason: "comment deletion cannot remove td logs or handoffs"}
	}
	latest, err := c.Get(ctx, issue.ID)
	if err != nil {
		return err
	}
	if latest.revision != issue.revision {
		return &ConflictError{ID: issue.ID}
	}
	again, err := c.request(ctx, "GET", path, nil, false)
	if err != nil {
		return err
	}
	var current apiComment
	if err := json.Unmarshal(again, &current); err != nil {
		return fmt.Errorf("invalid GitHub comment response: %w", err)
	}
	if current != before {
		return &WorkflowStateError{Reason: "comment changed before deletion; refresh before retrying"}
	}
	if _, err := c.request(ctx, "DELETE", path, nil, false); err != nil {
		return fmt.Errorf("delete %s on %s failed or is uncertain; inspect current comments before retrying: %w", canonical, issue.ID, err)
	}
	remaining, err := c.ListActivity(ctx, issue.ID)
	if err != nil {
		return fmt.Errorf("delete %s was accepted, but verification failed; inspect current comments before retrying: %w", canonical, err)
	}
	for _, a := range remaining {
		if a.ID == canonical {
			return &WorkflowStateError{Reason: fmt.Sprintf("delete %s was accepted but the comment is still visible; inspect before retrying", canonical)}
		}
	}
	return nil
}

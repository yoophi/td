package ghstore

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/marcus/td/internal/models"
)

type UndoScopeError struct{ Reason string }

func (e *UndoScopeError) Error() string { return e.Reason }

type ActionUndoResult struct {
	Issue         *Record              `json:"issue"`
	Journal       *ActionJournalRecord `json:"journal"`
	AlreadyUndone bool                 `json:"already_undone"`
}

// UndoAction rehydrates the remote receipt rather than trusting a caller's
// snapshots. Scalar issue and auxiliary configuration restoration are supported; workflow/graph and
// multi-target operations require their own coordinated executor.
func (c *Client) UndoAction(ctx context.Context, id, sessionID string) (*ActionUndoResult, error) {
	number, err := CommentNumber(id)
	if err != nil {
		return nil, err
	}
	data, err := c.request(ctx, "GET", fmt.Sprintf("/issues/comments/%d", number), nil, false)
	if err != nil {
		return nil, err
	}
	var comment apiComment
	if err := json.Unmarshal(data, &comment); err != nil {
		return nil, err
	}
	carrier, err := c.commentIssueNumber(comment.IssueURL)
	if err != nil || comment.ID != number {
		return nil, fmt.Errorf("undo journal response identity is invalid")
	}
	action, err := c.decodeJournalResponse(data, comment.Body, fmt.Sprintf("gh-%d", carrier))
	if err != nil {
		return nil, err
	}
	if action == nil {
		return nil, fmt.Errorf("comment %s is not an action journal", id)
	}
	if action.CarrierID == "" {
		return nil, &UndoScopeError{Reason: "legacy per-target journal execution requires migration to a separate carrier; no target write attempted"}
	}
	if _, err := c.journalCarrier(ctx, action.CarrierID); err != nil {
		return nil, err
	}
	profile, err := c.requestAPI(ctx, "GET", "user", "/user", nil, false)
	if err != nil {
		return nil, err
	}
	var user struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(profile, &user); err != nil || user.Login == "" {
		return nil, fmt.Errorf("GitHub actor identity is unverified")
	}
	current, err := c.readJournalTarget(ctx, action.IssueID)
	if err != nil {
		return nil, err
	}
	if action.Action == "comment_create" {
		return c.undoCommentAction(ctx, action, current, sessionID, user.Login)
	}
	plan, witnesses, err := c.planUndo(ctx, action, current, sessionID, user.Login)
	if err != nil {
		return nil, err
	}
	if plan.AlreadyUndone {
		return &ActionUndoResult{Issue: current, Journal: action, AlreadyUndone: true}, nil
	}
	if err := scalarUndoScope(action); err != nil {
		return nil, err
	}
	if err := c.checkEntityUndoCatalog(ctx, action, false); err != nil {
		return nil, err
	}
	if action.entityKind() != "" {
		// Auxiliary writers restore configuration only, never native state/labels.
		delete(plan.Payload, "state")
		delete(plan.Payload, "labels")
	}
	pending := action.ActionJournal
	pending.Phase = "undo_prepared"
	pending.UndoOperationID = "td-op-" + rand.Text()
	prepared, err := c.rewriteActionJournal(ctx, action, pending)
	if err != nil {
		return nil, fmt.Errorf("undo intent is unverified; no target write attempted: %w", err)
	}
	if err := c.recheckUndoWitnesses(ctx, witnesses); err != nil {
		return nil, err
	}
	if err := c.checkEntityUndoCatalog(ctx, action, false); err != nil {
		return nil, err
	}
	latest, err := c.readJournalTarget(ctx, current.ID)
	if err != nil {
		return nil, err
	}
	if latest.revision != current.revision {
		return nil, &ConflictError{ID: current.ID}
	}
	data, err = c.request(ctx, "PATCH", fmt.Sprintf("/issues/%d", current.Number), plan.Payload, false)
	if err != nil {
		return nil, fmt.Errorf("undo %s has an uncertain result; inspect target and receipt before retrying: %w", pending.UndoOperationID, err)
	}
	restored, err := decodeIssue(data)
	if err != nil {
		return nil, fmt.Errorf("undo response unreadable; inspect before retrying: %w", err)
	}
	if restored.Number != current.Number || restored.Title != action.Before.Title || restored.nativeBody != action.Before.Body || restored.nativeState != action.Before.State {
		return nil, fmt.Errorf("undo response does not match the inverse; inspect %s before retrying", current.ID)
	}
	if err := checkLabels(restored, action.Before.Labels, "updated"); err != nil {
		return nil, err
	}
	restored.repository = c.repo
	verified, err := c.readJournalTarget(withReadbackCost(ctx), current.ID)
	if err != nil {
		return nil, fmt.Errorf("undo was accepted but readback failed: %w", err)
	}
	if restored.revision != verified.revision {
		return nil, &ConflictError{ID: current.ID, AfterWrite: true}
	}
	state, err := actionIssueState(restored)
	if err != nil {
		return nil, err
	}
	if err := c.checkEntityUndoCatalog(ctx, action, true); err != nil {
		return nil, fmt.Errorf("undo target was accepted but board catalog verification failed; inspect before retrying: %w", err)
	}
	done := prepared.ActionJournal
	done.Phase = "undone"
	done.Restored = &state
	receipt, err := c.rewriteActionJournal(ctx, prepared, done)
	if err != nil {
		return nil, fmt.Errorf("undo target was verified but receipt is unverified; inspect %s before retrying: %w", pending.UndoOperationID, err)
	}
	latest, err = c.readJournalTarget(withReadbackCost(ctx), current.ID)
	if err != nil {
		return nil, err
	}
	if latest.revision != restored.revision {
		return nil, &ConflictError{ID: current.ID, AfterWrite: true}
	}
	if err := c.checkEntityUndoCatalog(ctx, action, true); err != nil {
		return nil, fmt.Errorf("undo receipt was verified but board catalog changed; inspect before retrying: %w", err)
	}
	return &ActionUndoResult{Issue: restored, Journal: receipt}, nil
}

func (c *Client) rewriteActionJournal(ctx context.Context, old *ActionJournalRecord, next ActionJournal) (*ActionJournalRecord, error) {
	oldBody, err := renderActionJournal(old.ActionJournal)
	if err != nil {
		return nil, err
	}
	newBody, err := renderActionJournal(next)
	if err != nil {
		return nil, err
	}
	if old.OperationID != next.OperationID || old.SessionID != next.SessionID || old.IssueID != next.IssueID || old.CarrierID != next.CarrierID {
		return nil, fmt.Errorf("journal rewrite cannot change action identity")
	}
	if old.Action != next.Action || !reflect.DeepEqual(old.Before, next.Before) || !reflect.DeepEqual(old.After, next.After) || !reflect.DeepEqual(old.CreateInput, next.CreateInput) || !reflect.DeepEqual(old.CommentInput, next.CommentInput) || !reflect.DeepEqual(old.CommentAfter, next.CommentAfter) {
		return nil, fmt.Errorf("undo receipt cannot rewrite original action snapshots")
	}
	allowedPhase := (old.Phase == "applied" && next.Phase == "undo_prepared") || (old.Phase == "undo_prepared" && next.Phase == "undone" && old.UndoOperationID == next.UndoOperationID)
	if !allowedPhase {
		return nil, fmt.Errorf("invalid undo receipt phase transition")
	}
	number, err := CommentNumber(old.ID)
	if err != nil {
		return nil, err
	}
	path := fmt.Sprintf("/issues/comments/%d", number)
	current, err := c.journalComment(ctx, path, nil, oldBody, old.CarrierID)
	if err != nil {
		return nil, err
	}
	if !sameJournalIdentity(old, current) || !old.UpdatedAt.Equal(current.UpdatedAt) {
		return nil, fmt.Errorf("journal changed before rewrite")
	}
	written, err := c.journalComment(ctx, path, map[string]string{"body": newBody}, newBody, old.CarrierID)
	if err != nil {
		return nil, err
	}
	if !sameJournalIdentity(old, written) {
		return nil, fmt.Errorf("journal rewrite identity changed")
	}
	verified, err := c.journalComment(withReadbackCost(ctx), path, nil, newBody, old.CarrierID)
	if err != nil {
		return nil, err
	}
	if !sameJournalIdentity(written, verified) || !written.UpdatedAt.Equal(verified.UpdatedAt) {
		return nil, fmt.Errorf("journal changed during rewrite verification")
	}
	return verified, nil
}

func scalarUndoScope(action *ActionJournalRecord) error {
	if action.entityKind() != "" {
		return entityUndoScope(action)
	}
	unsupported := func() error {
		return &UndoScopeError{Reason: "workflow, graph and multi-target undo integration is still pending; no target write attempted"}
	}
	if action.Action != "update" || action.Before == nil || action.After == nil || action.Before.State != action.After.State {
		return unsupported()
	}
	_, before, _, err := decodeBody(action.Before.Body)
	if err != nil {
		return err
	}
	_, after, _, err := decodeBody(action.After.Body)
	if err != nil {
		return err
	}
	if (before.EntityKind != "" && before.EntityKind != "issue") || (after.EntityKind != "" && after.EntityKind != "issue") || before.OperationID != after.OperationID || before.LastStateReason != after.LastStateReason {
		return unsupported()
	}
	stable := func(meta metadata, state string) IssueDetails {
		d := IssueDetails{Status: models.Status(state)}
		if meta.Details != nil {
			d = *meta.Details
		}
		if d.Status == "" {
			d.Status = models.Status(state)
		}
		d.Minor = false
		d.Sprint = ""
		d.DueDate = nil
		d.DeferUntil = nil
		d.DeferCount = 0
		return d
	}
	if !reflect.DeepEqual(stable(before, action.Before.State), stable(after, action.After.State)) {
		return unsupported()
	}
	return nil
}

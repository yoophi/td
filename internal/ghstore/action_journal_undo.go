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

type UndoScopeError struct{ Reason string }

func (e *UndoScopeError) Error() string { return e.Reason }

type ActionUndoResult struct {
	GroupMembers  []ActionGroupResultMember `json:"group_members,omitempty"`
	Compensation  *NativeIssueUndoPlan      `json:"compensation,omitempty"`
	UndoOutcome   *ActionUndoOutcome        `json:"undo_outcome,omitempty"`
	Issue         *Record                   `json:"issue"`
	Journal       *ActionJournalRecord      `json:"journal"`
	AlreadyUndone bool                      `json:"already_undone"`
}

// UndoAction rehydrates the remote receipt rather than trusting a caller's
// snapshots. Supported inverses restore scalar fields, files, relations,
// open-native workflow/review checkpoints and auxiliary configuration.
// Native close/reopen uses compensation; multi-target operations remain pending.
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
	if strings.Contains(comment.Body, journalArchivePrefix) || strings.Contains(comment.Body, journalArchivePartPrefix) {
		return nil, &UndoScopeError{Reason: "imported journal history is archival data, not a destination undo receipt; no write attempted"}
	}
	carrier, err := c.commentIssueNumber(comment.IssueURL)
	if err != nil || comment.ID != number {
		return nil, fmt.Errorf("undo journal response identity is invalid")
	}
	action, err := c.decodeJournalResponse(ctx, data, comment.Body, fmt.Sprintf("gh-%d", carrier))
	if err != nil {
		return nil, err
	}
	if action == nil {
		return nil, fmt.Errorf("comment %s is not an action journal", id)
	}
	if err := checkCreationChildAccess(ctx, action); err != nil {
		return nil, err
	}
	if action.CreationCommand != nil {
		return c.undoCreationCommand(ctx, action, sessionID)
	}
	if action.ImportCommand != nil {
		return c.undoImportCommand(ctx, action, sessionID)
	}
	if action.Group != nil {
		return c.undoActionGroup(ctx, action, sessionID)
	}
	if action.GroupOperationID != "" && groupUndoContext(ctx) != action.GroupOperationID {
		return nil, &UndoScopeError{Reason: "group member must be undone through its complete group; no target write attempted"}
	}
	if err := checkGroupUndoMember(ctx, action); err != nil {
		return nil, err
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
	if _, supported, err := nativeUndoScope(action); err != nil {
		return nil, err
	} else if supported || action.NativeUndo != nil {
		return c.undoNativeAction(ctx, action, current, sessionID, user.Login)
	}
	if action.Action == "comment_create" {
		return c.undoCommentAction(ctx, action, current, sessionID, user.Login)
	}
	plan, witnesses, err := c.planUndo(ctx, action, current, sessionID, user.Login)
	if err != nil {
		return nil, err
	}
	if plan.AlreadyUndone {
		graph, err := c.observeRelationUndoGraph(ctx, action, current, false)
		if err != nil {
			return nil, err
		}
		if err := c.checkUndoOriginalReview(ctx, action, false); err != nil {
			return nil, err
		}
		if err := c.recheckRelationUndoGraph(ctx, action, current, graph, false); err != nil {
			return nil, err
		}
		workflowChange, err := workflowUndoScope(action)
		if err == nil && !workflowChange {
			workflowChange, err = reviewTransitionUndoScope(action)
		}
		if err == nil && !workflowChange {
			workflowChange, err = autoParentReviewUndoScope(action)
		}
		if err == nil && !workflowChange {
			workflowChange, err = deletionUndoScope(action)
		}
		if err != nil {
			return nil, err
		}
		if fileChange, err := fileUndoScope(action); err != nil {
			return nil, err
		} else if fileChange || graph != nil || workflowChange {
			latest, err := c.readJournalTarget(ctx, current.ID)
			if err != nil {
				return nil, err
			}
			if latest.revision != current.revision {
				return nil, &ConflictError{ID: current.ID}
			}
		}
		return &ActionUndoResult{Issue: current, Journal: action, AlreadyUndone: true, UndoOutcome: observedForwardInverseOutcome(action)}, nil
	}
	if err := scalarUndoScope(action); err != nil {
		return nil, err
	}
	graph, err := c.observeRelationUndoGraph(ctx, action, current, false)
	if err != nil {
		return nil, err
	}
	if err := c.checkUndoOriginalReview(ctx, action, false); err != nil {
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
	if err := c.checkUndoOriginalReview(ctx, action, false); err != nil {
		return nil, err
	}
	if err := c.recheckRelationUndoGraph(ctx, action, current, graph, false); err != nil {
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
	if err := checkGroupUndoGate(ctx); err != nil {
		return nil, err
	}
	if err := checkGroupUpdateUndoGate(ctx, prepared, current); err != nil {
		return nil, err
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
	if err := c.checkUndoOriginalReview(ctx, action, true); err != nil {
		return nil, err
	}
	if err := c.recheckRelationUndoGraph(ctx, action, restored, graph, true); err != nil {
		return nil, err
	}
	if err := c.checkEntityUndoCatalog(ctx, action, true); err != nil {
		return nil, fmt.Errorf("undo target was accepted but board catalog verification failed; inspect before retrying: %w", err)
	}
	done := prepared.ActionJournal
	done.Phase = "undone"
	done.Restored = &state
	if err := checkGroupUpdateUndoGate(ctx, prepared, restored); err != nil {
		return nil, err
	}
	receipt, err := c.rewriteActionJournal(ctx, prepared, done)
	if err != nil {
		return nil, fmt.Errorf("undo target was verified but receipt is unverified; inspect %s before retrying: %w", pending.UndoOperationID, err)
	}
	if err := c.checkUndoOriginalReview(ctx, action, true); err != nil {
		return nil, err
	}
	if err := c.recheckRelationUndoGraph(ctx, action, restored, graph, true); err != nil {
		return nil, err
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
	if err := checkGroupUpdateUndoGate(ctx, receipt, restored); err != nil {
		return nil, err
	}
	return &ActionUndoResult{Issue: restored, Journal: receipt, UndoOutcome: observedForwardInverseOutcome(receipt)}, nil
}

func (c *Client) rewriteActionJournal(ctx context.Context, old *ActionJournalRecord, next ActionJournal) (*ActionJournalRecord, error) {
	return c.rewriteActionJournalGuarded(ctx, old, next, nil)
}

func (c *Client) rewriteActionJournalGuarded(ctx context.Context, old *ActionJournalRecord, next ActionJournal, fence func() error) (*ActionJournalRecord, error) {
	oldBody, err := renderExistingActionJournal(old)
	if err != nil {
		return nil, err
	}
	publication, err := planActionJournalPublication(next)
	if err != nil {
		return nil, err
	}
	if old.OperationID != next.OperationID || old.SessionID != next.SessionID || old.IssueID != next.IssueID || old.CarrierID != next.CarrierID {
		return nil, fmt.Errorf("journal rewrite cannot change action identity")
	}
	if !reflect.DeepEqual(old.CommentObservation, next.CommentObservation) {
		return nil, fmt.Errorf("undo cannot rewrite the original comment observation")
	}
	if old.Action != next.Action || !reflect.DeepEqual(old.Before, next.Before) || !reflect.DeepEqual(old.After, next.After) || !reflect.DeepEqual(old.CreateInput, next.CreateInput) || !reflect.DeepEqual(old.CommentInput, next.CommentInput) || !reflect.DeepEqual(old.CommentAfter, next.CommentAfter) || !reflect.DeepEqual(old.CommentBefore, next.CommentBefore) || !reflect.DeepEqual(old.UpdateObservation, next.UpdateObservation) || !reflect.DeepEqual(old.UpdateIntent, next.UpdateIntent) || !reflect.DeepEqual(old.Reconciliation, next.Reconciliation) || !reflect.DeepEqual(old.UndoObservation, next.UndoObservation) {
		return nil, fmt.Errorf("undo receipt cannot rewrite original action snapshots")
	}
	if err := validateGroupRewrite(old.ActionJournal, next); err != nil {
		return nil, err
	}
	if err := validateNativeUndoRewrite(old.NativeUndo, next.NativeUndo); err != nil {
		return nil, err
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
	if fence != nil {
		if err := fence(); err != nil {
			return nil, err
		}
	}
	verified, err := c.publishJournal(ctx, path, publication, old, fence)
	if err != nil {
		return nil, err
	}
	if !sameJournalIdentity(old, verified) {
		return nil, fmt.Errorf("journal rewrite identity changed")
	}
	return verified, nil
}

func scalarUndoScope(action *ActionJournalRecord) error {
	if action.entityKind() != "" {
		return entityUndoScope(action)
	}
	if supported, err := fileUndoScope(action); err != nil || supported {
		return err
	}
	if supported, err := relationUndoScope(action); err != nil || supported {
		return err
	}
	if supported, err := workflowUndoScope(action); err != nil || supported {
		return err
	}
	if supported, err := reviewTransitionUndoScope(action); err != nil || supported {
		return err
	}
	if supported, err := autoParentReviewUndoScope(action); err != nil || supported {
		return err
	}
	if supported, err := deletionUndoScope(action); err != nil || supported {
		return err
	}
	return scalarFieldUndoScope(action)
}

// Scalar batches do not inherit the separately integrated workflow, relation,
// deletion or auxiliary-entity scopes above.
func scalarFieldUndoScope(action *ActionJournalRecord) error {
	unsupported := func() error {
		return &UndoScopeError{Reason: "workflow and coordinated multi-target undo integration is still pending; no target write attempted"}
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

// A new inverse can be verified even when the original forward write was only
// observed. It must not become a causal bridge for an earlier action.
func observedForwardInverseOutcome(a *ActionJournalRecord) *ActionUndoOutcome {
	if a.UpdateObservation == nil && a.CommentObservation == nil {
		return nil
	}
	if a.UndoObservation != nil {
		return &ActionUndoOutcome{Proof: inverseObservationProof}
	}
	return &ActionUndoOutcome{Proof: "verified-undo-receipt", TargetWriteVerified: true}
}

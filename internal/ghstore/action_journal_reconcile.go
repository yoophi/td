package ghstore

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"
)

// Reconciliation records a new observation, preserving the original attempt's
// attribution and immutable snapshots. It does not claim to have replayed it.
type ActionReconciliation struct {
	SessionID  string            `json:"session_id"`
	Actor      string            `json:"actor"`
	ObservedAt time.Time         `json:"observed_at"`
	State      *ActionIssueState `json:"state"`
}

func (a ActionJournal) validateReconciliation() error {
	r := a.Reconciliation
	if r == nil {
		return nil
	}
	if a.Phase != "applied" || (a.Action != "create" && a.Action != "comment_delete") || r.State == nil || r.Actor == "" || r.ObservedAt.IsZero() {
		return fmt.Errorf("reconciliation requires an observed creation/deletion result")
	}
	probe := ActionJournal{OperationID: "probe", SessionID: r.SessionID, IssueID: "gh-1", Action: "create", Phase: "prepared"}
	if err := probe.validate(); err != nil {
		return fmt.Errorf("invalid reconciliation session: %w", err)
	}
	if err := r.State.validate(); err != nil {
		return err
	}
	if !sameActionContents(*a.After, *r.State) || r.State.UpdatedAt.Before(a.After.UpdatedAt) {
		return fmt.Errorf("reconciliation cannot replace original after-state fields or reverse its clock")
	}
	return nil
}

type ActionReconcileResult struct {
	Issue               *Record              `json:"issue"`
	Journal             *ActionJournalRecord `json:"journal"`
	Applied             bool                 `json:"applied"`
	AlreadyReconciled   bool                 `json:"already_reconciled"`
	UndoOutcome         *ActionUndoOutcome   `json:"undo_outcome,omitempty"`
	TargetWriteReplayed bool                 `json:"target_write_replayed"`
}

// ReconcileAction previews by default. Explicit apply changes only the existing
// journal, never POSTs an issue or replays PATCH/DELETE on the target. The native
// author must match; the recovery session is recorded separately from the intent.
func (c *Client) ReconcileAction(ctx context.Context, id, sessionID string, apply bool) (*ActionReconcileResult, error) {
	probe := ActionJournal{OperationID: "probe", SessionID: sessionID, IssueID: "gh-1", Action: "create", Phase: "prepared"}
	if err := probe.validate(); err != nil {
		return nil, err
	}
	number, err := CommentNumber(id)
	if err != nil {
		return nil, err
	}
	path := fmt.Sprintf("/issues/comments/%d", number)
	data, err := c.request(ctx, "GET", path, nil, false)
	if err != nil {
		return nil, err
	}
	var native apiComment
	if err := json.Unmarshal(data, &native); err != nil {
		return nil, err
	}
	carrier, err := c.commentIssueNumber(native.IssueURL)
	if err != nil || native.ID != number {
		return nil, fmt.Errorf("recovery receipt membership is unverified")
	}
	action, err := c.decodeJournalResponse(data, native.Body, fmt.Sprintf("gh-%d", carrier))
	if err != nil {
		return nil, err
	}
	if action == nil || action.CarrierID == "" {
		return nil, fmt.Errorf("recovery requires a separate action journal carrier")
	}
	if _, err := c.journalCarrier(ctx, action.CarrierID); err != nil {
		return nil, err
	}
	profile, err := c.requestAPI(ctx, "GET", "user", "/user", nil, false)
	if err != nil {
		return nil, err
	}
	var actor struct{ Login string }
	if err := json.Unmarshal(profile, &actor); err != nil {
		return nil, err
	}
	if actor.Login == "" || !strings.EqualFold(actor.Login, action.Author) {
		return nil, fmt.Errorf("recovery receipt is not attributed to the authenticated GitHub author; no write attempted")
	}
	if action.Phase == "undo_prepared" || action.Phase == "undone" {
		return c.reconcileUndo(ctx, action, sessionID, actor.Login, apply)
	}
	if action.Phase != "prepared" && action.Phase != "applied" {
		return nil, &UndoScopeError{Reason: "undo recovery needs its own inverse proof; no write attempted"}
	}
	if action.Action != "create" && action.Action != "comment_delete" {
		return nil, &UndoScopeError{Reason: "this action needs its own recovery proof; no write attempted"}
	}
	current, err := c.observeReconcileResult(ctx, action)
	if err != nil {
		return nil, err
	}
	state, err := actionIssueState(current)
	if err != nil {
		return nil, err
	}
	result := &ActionReconcileResult{Issue: current, Journal: action, AlreadyReconciled: action.Reconciliation != nil}
	if action.Reconciliation != nil {
		if state.Revision != action.Reconciliation.State.Revision {
			return nil, &ConflictError{ID: current.ID}
		}
		return result, nil
	}
	if !apply {
		return result, nil
	}
	// Recheck target and the original receipt immediately before the journal PATCH.
	latest, err := c.readJournalTarget(ctx, current.ID)
	if err != nil {
		return nil, err
	}
	if latest.revision != current.revision {
		return nil, &ConflictError{ID: current.ID}
	}
	oldBody, err := renderActionJournal(action.ActionJournal)
	if err != nil {
		return nil, err
	}
	original, err := c.journalComment(ctx, path, nil, oldBody, action.CarrierID)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(original, action) {
		return nil, fmt.Errorf("recovery receipt changed; no write attempted")
	}
	resolved := action.ActionJournal
	resolved.Phase = "applied"
	if action.Phase == "prepared" {
		resolved.After = &state
		resolved.IssueID = current.ID
	}
	resolved.Reconciliation = &ActionReconciliation{SessionID: sessionID, Actor: actor.Login, ObservedAt: time.Now().UTC(), State: &state}
	body, err := renderActionJournal(resolved)
	if err != nil {
		return nil, err
	}
	written, err := c.journalComment(ctx, path, map[string]string{"body": body}, body, action.CarrierID)
	if err != nil {
		return nil, fmt.Errorf("recovery receipt %s is unverified; inspect before retrying (no target write replayed): %w", action.ID, err)
	}
	if !sameJournalIdentity(action, written) {
		return nil, fmt.Errorf("recovery receipt identity changed; inspect before retrying")
	}
	verified, err := c.journalComment(withReadbackCost(ctx), path, nil, body, action.CarrierID)
	if err != nil {
		return nil, fmt.Errorf("recovery receipt readback is unverified; inspect before retrying: %w", err)
	}
	if !reflect.DeepEqual(written, verified) {
		return nil, fmt.Errorf("recovery receipt changed during readback; inspect before retrying")
	}
	observed, err := c.observeReconcileResult(ctx, verified)
	if err != nil {
		return nil, fmt.Errorf("recovery receipt saved but final observation failed; inspect before retrying: %w", err)
	}
	if observed.revision != current.revision {
		return nil, &ConflictError{ID: current.ID, AfterWrite: true}
	}
	result.Journal, result.Applied = verified, true
	return result, nil
}

func (c *Client) observeReconcileResult(ctx context.Context, action *ActionJournalRecord) (*Record, error) {
	snapshot, err := c.ReadSnapshot(WithFreshSnapshot(ctx), true)
	if err != nil {
		return nil, err
	}
	history, err := snapshot.ActionHistory("")
	if err != nil {
		return nil, err
	}
	carriers, found := 0, false
	for _, record := range snapshot.records {
		if record.meta.EntityKind == "journal" {
			carriers++
			if record.ID != action.CarrierID {
				return nil, fmt.Errorf("recovery carrier is ambiguous")
			}
		}
	}
	for _, observed := range history {
		if observed.ID == action.ID {
			if !reflect.DeepEqual(observed, *action) {
				return nil, fmt.Errorf("recovery journal changed during observation")
			}
			found = true
		}
	}
	if carriers != 1 || !found {
		return nil, fmt.Errorf("recovery requires one observed carrier and its exact journal")
	}
	var target *Record
	if action.Action == "create" {
		if action.CreateInput == nil {
			return nil, fmt.Errorf("legacy creation has no exact recorded input; no write attempted")
		}
		for i := range snapshot.records {
			r := &snapshot.records[i]
			if r.meta.OperationID == action.OperationID {
				if target != nil {
					return nil, fmt.Errorf("creation operation has multiple results; no write attempted")
				}
				target = r
			}
		}
		if target == nil {
			return nil, fmt.Errorf("creation result not observed; no POST replayed")
		}
		if action.IssueID != "" && action.IssueID != target.ID {
			return nil, fmt.Errorf("creation result identity changed")
		}
		if target.Title != action.CreateInput.Title || target.nativeBody != action.CreateInput.Body || target.nativeState != "open" {
			return nil, &ConflictError{ID: target.ID}
		}
		if err := checkLabels(target, action.CreateInput.Labels, "observed"); err != nil {
			return nil, err
		}
		for _, entry := range snapshot.entries {
			if entry.Issue.Number == target.Number && !strings.EqualFold(entry.Issue.User.Login, action.Author) {
				return nil, fmt.Errorf("creation result author differs from intent author")
			}
		}
	} else {
		for i := range snapshot.records {
			if snapshot.records[i].ID == action.IssueID {
				target = &snapshot.records[i]
			}
		}
		if target == nil {
			return nil, fmt.Errorf("deletion target not observed")
		}
		state, err := actionIssueState(target)
		if err != nil {
			return nil, err
		}
		if !sameActionContents(*action.Before, state) || state.UpdatedAt.Before(action.Before.UpdatedAt) {
			return nil, &ConflictError{ID: target.ID}
		}
		for _, entry := range snapshot.entries {
			for _, comment := range entry.Comments {
				if fmt.Sprintf("ghc-%d", comment.ID) == action.CommentBefore.ID {
					return nil, fmt.Errorf("original comment still exists; no DELETE replayed")
				}
			}
		}
	}
	point, err := c.readJournalTarget(withReadbackCost(ctx), target.ID)
	if err != nil {
		return nil, err
	}
	if point.revision != target.revision {
		return nil, &ConflictError{ID: target.ID}
	}
	return point, nil
}

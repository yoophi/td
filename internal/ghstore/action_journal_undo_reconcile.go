package ghstore

import (
	"context"
	"fmt"
	"reflect"
	"time"
)

const inverseObservationProof = "inverse-contents-observed"

// An observation resolves an uncertain receipt without asserting which actor
// performed the inverse. It must never authorize a causal undo chain.
type ActionUndoObservation struct {
	SessionID  string            `json:"session_id"`
	Actor      string            `json:"actor"`
	ObservedAt time.Time         `json:"observed_at"`
	Proof      string            `json:"proof"`
	State      *ActionIssueState `json:"state"`
}

type ActionUndoOutcome struct {
	Proof                   string `json:"proof"`
	TargetWriteVerified     bool   `json:"target_write_verified"`
	CausalUndoChainEligible bool   `json:"causal_undo_chain_eligible"`
}

func (a ActionJournal) validateUndoObservation() error {
	o := a.UndoObservation
	if o == nil {
		return nil
	}
	if a.Phase != "undone" || a.Before == nil || a.After == nil || a.Restored == nil || a.Reconciliation != nil || o.Actor == "" || o.ObservedAt.IsZero() || o.State == nil || o.Proof != inverseObservationProof {
		return fmt.Errorf("undo observation requires an explicit observed inverse result")
	}
	probe := ActionJournal{OperationID: "probe", SessionID: o.SessionID, IssueID: "gh-1", Action: "create", Phase: "prepared"}
	if err := probe.validate(); err != nil {
		return err
	}
	if !reflect.DeepEqual(o.State, a.Restored) || !sameActionContents(*a.Before, *o.State) || o.State.UpdatedAt.Before(a.After.UpdatedAt) {
		return fmt.Errorf("undo observation cannot rewrite the original inverse or reverse its clock")
	}
	return nil
}

func (c *Client) observeUndoReconcile(ctx context.Context, a *ActionJournalRecord) (*Record, error) {
	if a.Action != "comment_create" {
		if err := scalarUndoScope(a); err != nil {
			return nil, err
		}
	}
	snapshot, err := c.ReadSnapshot(WithFreshSnapshot(ctx), true)
	if err != nil {
		return nil, err
	}
	history, err := snapshot.ActionHistory("")
	if err != nil {
		return nil, err
	}
	found, carriers := 0, 0
	for _, r := range snapshot.records {
		if r.meta.EntityKind == "journal" {
			carriers++
			if r.ID != a.CarrierID {
				return nil, fmt.Errorf("undo recovery carrier is ambiguous")
			}
		}
	}
	for _, r := range history {
		if r.ID == a.ID {
			found++
			if !reflect.DeepEqual(r, *a) {
				return nil, fmt.Errorf("undo recovery receipt changed during observation")
			}
		}
	}
	if found != 1 || carriers != 1 {
		return nil, fmt.Errorf("undo recovery requires one observed carrier and its exact receipt")
	}
	var target *Record
	for i := range snapshot.records {
		if snapshot.records[i].ID == a.IssueID {
			target = &snapshot.records[i]
		}
	}
	if target == nil {
		return nil, fmt.Errorf("undo recovery target not observed")
	}
	state, err := actionIssueState(target)
	if err != nil {
		return nil, err
	}
	if !sameActionContents(*a.Before, state) || state.UpdatedAt.Before(a.After.UpdatedAt) || a.Phase == "undone" && state.Revision != a.Restored.Revision {
		return nil, &ConflictError{ID: a.IssueID}
	}
	if a.Action == "comment_create" {
		if err := deletedCommentInSnapshot(snapshot, a); err != nil {
			return nil, err
		}
	}
	point, err := c.readJournalTarget(withReadbackCost(ctx), a.IssueID)
	if err != nil {
		return nil, err
	}
	if point.revision != target.revision {
		return nil, &ConflictError{ID: a.IssueID}
	}
	return point, nil
}

// Only the journal may be written. Original snapshots and undo identity are
// immutable; matching inverse contents are recorded as an observation, not as
// evidence that this actor performed the original PATCH/DELETE.
func (c *Client) reconcileUndo(ctx context.Context, a *ActionJournalRecord, session, actor string, apply bool) (*ActionReconcileResult, error) {
	current, err := c.observeUndoReconcile(ctx, a)
	if err != nil {
		return nil, err
	}
	graph, err := c.observeRelationUndoGraph(ctx, a, current, false)
	if err != nil {
		return nil, err
	}
	checkProof := func(record *Record) error {
		if err := c.checkUndoOriginalReview(ctx, a, false); err != nil {
			return err
		}
		if err := c.recheckRelationUndoGraph(ctx, a, record, graph, false); err != nil {
			return err
		}
		if err := c.checkEntityUndoCatalog(ctx, a, false); err != nil {
			return err
		}
		latest, err := c.readJournalTarget(ctx, a.IssueID)
		if err != nil {
			return err
		}
		if latest.revision != record.revision {
			return &ConflictError{ID: a.IssueID}
		}
		return nil
	}
	if err := checkProof(current); err != nil {
		return nil, err
	}
	result := &ActionReconcileResult{Issue: current, Journal: a, AlreadyReconciled: a.Phase == "undone", UndoOutcome: &ActionUndoOutcome{Proof: inverseObservationProof}}
	if a.Phase == "undone" && a.UndoObservation == nil {
		result.UndoOutcome = &ActionUndoOutcome{Proof: "verified-undo-receipt", TargetWriteVerified: true, CausalUndoChainEligible: a.UpdateObservation == nil && a.CommentObservation == nil}
	}
	if a.Phase == "undone" || !apply {
		return result, nil
	}
	oldBody, err := renderExistingActionJournal(a)
	if err != nil {
		return nil, err
	}
	number, err := CommentNumber(a.ID)
	if err != nil {
		return nil, err
	}
	path := fmt.Sprintf("/issues/comments/%d", number)
	original, err := c.journalComment(ctx, path, nil, oldBody, a.CarrierID)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(original, a) {
		return nil, fmt.Errorf("undo recovery receipt changed; no target write attempted")
	}
	if err := checkProof(current); err != nil {
		return nil, err
	}
	state, err := actionIssueState(current)
	if err != nil {
		return nil, err
	}
	resolved := a.ActionJournal
	resolved.Phase = "undone"
	resolved.Restored = &state
	resolved.UndoObservation = &ActionUndoObservation{SessionID: session, Actor: actor, ObservedAt: time.Now().UTC(), Proof: inverseObservationProof, State: &state}
	publication, err := planActionJournalPublication(resolved)
	if err != nil {
		return nil, err
	}
	verified, err := c.publishJournal(ctx, path, publication, a, nil)
	if err != nil {
		return nil, fmt.Errorf("undo recovery receipt is unverified; no target write replayed: %w", err)
	}
	if !sameJournalIdentity(a, verified) {
		return nil, fmt.Errorf("undo recovery receipt identity changed; inspect before retrying")
	}
	observed, err := c.observeUndoReconcile(ctx, verified)
	if err != nil {
		return nil, fmt.Errorf("undo recovery receipt saved but observation failed; no target replay: %w", err)
	}
	if observed.revision != current.revision {
		return nil, &ConflictError{ID: a.IssueID}
	}
	if err := checkProof(observed); err != nil {
		return nil, fmt.Errorf("undo recovery receipt saved but proof changed: %w", err)
	}
	result.Journal, result.Applied = verified, true
	return result, nil
}

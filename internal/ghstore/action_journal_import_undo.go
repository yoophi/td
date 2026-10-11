package ghstore

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"
)

func importInverseStates(root *ActionJournalRecord) []ImportNativeState {
	states := slices.Clone(root.ImportForward.Targets)
	if inverse := root.ImportInverse; inverse != nil {
		for n, result := range inverse.Results {
			states[inverse.Steps[n].Member] = result.State
		}
	}
	return states
}

func sameImportNativeState(repository string, a, b ImportNativeState) bool {
	left, err := canonicalImportDestination(repository, a.Issue, a.Comments)
	if err != nil {
		return false
	}
	right, err := canonicalImportDestination(repository, b.Issue, b.Comments)
	return err == nil && bytes.Equal(left, right) && reflect.DeepEqual(a.Events, b.Events)
}

// A pending result may be observed, but never replayed. Every other selected
// task, preserved comment and reachable outside graph revision stays exact.
func (c *Client) observeImportInverse(ctx context.Context, root *ActionJournalRecord, allowPendingResult bool) (*ImportInverseResult, error) {
	snapshot, err := c.ReadSnapshot(WithFreshSnapshot(ctx), true)
	if err != nil {
		return nil, err
	}
	history, err := snapshot.ActionHistory("")
	if err != nil {
		return nil, err
	}
	found := false
	for _, actual := range history {
		if actual.ID == root.ID {
			found = reflect.DeepEqual(actual, *root)
		}
	}
	if !found {
		return nil, fmt.Errorf("original import inverse root changed or disappeared")
	}
	inverse := root.ImportInverse
	planned, err := planImportInverse(root.ActionJournal, snapshot, root.UndoOperationID, inverse.At)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(planned.Steps, inverse.Steps) || !reflect.DeepEqual(planned.Watches, inverse.Watches) {
		return nil, fmt.Errorf("import inverse graph changed after preparation")
	}
	entries := map[int]snapshotEntry{}
	for _, entry := range snapshot.entries {
		entries[entry.Issue.Number] = entry
	}
	var pending *ImportInverseResult
	for n, expected := range importInverseStates(root) {
		entry, found := entries[expected.Issue.Number]
		if !found {
			return nil, &ConflictError{ID: fmt.Sprintf("gh-%d", expected.Issue.Number)}
		}
		events, err := c.readStateEvents(ctx, fmt.Sprintf("gh-%d", expected.Issue.Number))
		if err != nil {
			return nil, err
		}
		actual := ImportNativeState{Issue: entry.Issue, Comments: append([]apiComment{}, entry.Comments...), Events: events}
		if sameImportNativeState(c.repo, expected, actual) {
			continue
		}
		if allowPendingResult && inverse.Started == len(inverse.Results)+1 {
			step := inverse.Steps[len(inverse.Results)]
			if step.Member == n {
				candidate := ImportInverseResult{State: actual, Observation: CreationObservation{Proof: "observed", SessionID: root.SessionID, Actor: root.Author, ObservedAt: time.Now().UTC()}}
				if err := validateImportInverseResult(root.ActionJournal, expected, step, candidate); err != nil {
					return nil, err
				}
				pending = &candidate
				continue
			}
		}
		return nil, &ConflictError{ID: fmt.Sprintf("gh-%d", expected.Issue.Number)}
	}
	return pending, nil
}

func (c *Client) writeImportInverseReceipt(ctx context.Context, old *ActionJournalRecord, next ActionJournal, fence func() error) (*ActionJournalRecord, error) {
	if err := validateImportInverseRewrite(old.ActionJournal, next.Phase, next.UndoOperationID, next.ImportInverse); err != nil {
		return nil, err
	}
	publication, err := planActionJournalPublication(next)
	if err != nil {
		return nil, err
	}
	n, _ := CommentNumber(old.ID)
	return c.publishJournal(ctx, fmt.Sprintf("/issues/comments/%d", n), publication, old, fence)
}

func importInverseOutcome(root *ActionJournalRecord) *ActionUndoOutcome {
	verified := root.ImportForward.Observation.Proof == "verified" && root.ImportInverse.Result != nil && root.ImportInverse.Result.Proof == "verified"
	proof := "import-compensation-observed"
	if verified {
		proof = "verified-import-compensation"
	}
	return &ActionUndoOutcome{Proof: proof, TargetWriteVerified: verified, CausalUndoChainEligible: false}
}

func importUndoResult(root *ActionJournalRecord, already bool) (*ActionUndoResult, error) {
	states := importInverseStates(root)
	result := &ActionUndoResult{Journal: root, AlreadyUndone: already, UndoOutcome: importInverseOutcome(root)}
	for _, native := range states {
		record, err := native.Issue.record()
		if err != nil {
			return nil, err
		}
		if result.Issue == nil {
			result.Issue = record
		}
		state, err := actionIssueState(record)
		if err != nil {
			return nil, err
		}
		result.GroupMembers = append(result.GroupMembers, ActionGroupResultMember{IssueID: record.ID, Status: "undone", State: state})
	}
	return result, nil
}

func (c *Client) undoImportCommand(ctx context.Context, root *ActionJournalRecord, session string) (*ActionUndoResult, error) {
	if root.SessionID != session {
		return nil, fmt.Errorf("import undo requires its original session %s", root.SessionID)
	}
	if root.Phase == "prepared" {
		return nil, fmt.Errorf("import forward result is incomplete; reconcile or finish %s with its original input before undo", root.ID)
	}
	profile, err := c.requestAPI(ctx, "GET", "user", "/user", nil, false)
	if err != nil {
		return nil, err
	}
	var actor struct{ Login string }
	if json.Unmarshal(profile, &actor) != nil || actor.Login == "" || !strings.EqualFold(actor.Login, root.Author) {
		return nil, fmt.Errorf("import undo requires its original GitHub author")
	}
	if root.Phase == "applied" {
		snapshot, err := c.ReadSnapshot(WithFreshSnapshot(ctx), true)
		if err != nil {
			return nil, err
		}
		if err := c.verifyCompletedImport(ctx, root, snapshot); err != nil {
			return nil, err
		}
		next, err := copyCreationJournal(root.ActionJournal)
		if err != nil {
			return nil, err
		}
		next.Phase, next.UndoOperationID = "undo_prepared", "td-op-"+rand.Text()
		next.ImportInverse, err = planImportInverse(next, snapshot, next.UndoOperationID, time.Now().UTC())
		if err != nil {
			return nil, err
		}
		if err := checkImportInverseCapacity(next); err != nil {
			return nil, err
		}
		fence := func() error {
			fresh, err := c.ReadSnapshot(WithFreshSnapshot(ctx), true)
			if err != nil {
				return err
			}
			if err := c.verifyCompletedImport(ctx, root, fresh); err != nil {
				return err
			}
			actual, err := planImportInverse(next, fresh, next.UndoOperationID, next.ImportInverse.At)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(actual, next.ImportInverse) {
				return fmt.Errorf("import inverse graph changed during preparation")
			}
			return nil
		}
		root, err = c.writeImportInverseReceipt(ctx, root, next, fence)
		if err != nil {
			return nil, err
		}
	}
	if root.Phase == "undone" {
		if _, err := c.observeImportInverse(ctx, root, false); err != nil {
			return nil, err
		}
		return importUndoResult(root, true)
	}
	for len(root.ImportInverse.Results) < len(root.ImportInverse.Steps) {
		if root.ImportInverse.Started != len(root.ImportInverse.Results) {
			return nil, fmt.Errorf("import inverse %s has an uncertain step; use undo --reconcile %s --apply; no target write replayed", root.UndoOperationID, root.ID)
		}
		fence := func() error { _, err := c.observeImportInverse(ctx, root, false); return err }
		next, err := copyCreationJournal(root.ActionJournal)
		if err != nil {
			return nil, err
		}
		next.ImportInverse.Started++
		root, err = c.writeImportInverseReceipt(ctx, root, next, fence)
		if err != nil {
			return nil, err
		}
		if _, err := c.observeImportInverse(ctx, root, false); err != nil {
			return nil, c.cancelImportInverseBeforeWrite(ctx, root, session, actor.Login, "snapshot", err)
		}
		index := len(root.ImportInverse.Results)
		step := root.ImportInverse.Steps[index]
		before := importInverseStates(root)[step.Member]
		id := fmt.Sprintf("gh-%d", before.Issue.Number)
		live, _, err := c.importNativeTarget(ctx, id)
		if err != nil {
			return nil, c.cancelImportInverseBeforeWrite(ctx, root, session, actor.Login, "target", err)
		}
		if !sameImportNativeState(c.repo, ImportNativeState{Issue: before.Issue}, ImportNativeState{Issue: live}) {
			return nil, c.cancelImportInverseBeforeWrite(ctx, root, session, actor.Login, "conflict", &ConflictError{ID: id})
		}
		payload := map[string]any{"title": step.Input.Title, "body": step.Input.Body, "state": step.Input.State, "labels": step.Input.Labels}
		data, err := c.request(ctx, "PATCH", fmt.Sprintf("/issues/%d", before.Issue.Number), payload, false)
		if err != nil {
			return nil, fmt.Errorf("import inverse %s step %d result is uncertain; inspect/reconcile without replay: %w", root.ID, index+1, err)
		}
		var response apiIssue
		if err := json.Unmarshal(data, &response); err != nil {
			return nil, fmt.Errorf("import inverse response is unreadable; reconcile %s: %w", root.ID, err)
		}
		observed, err := c.observeImportInverse(ctx, root, true)
		if err != nil {
			return nil, err
		}
		if observed == nil || !sameImportNativeState(c.repo, ImportNativeState{Issue: response}, ImportNativeState{Issue: observed.State.Issue}) {
			return nil, fmt.Errorf("import inverse response differs from fresh native evidence; reconcile %s", root.ID)
		}
		observed.Observation = CreationObservation{Proof: "verified", SessionID: session, Actor: actor.Login, ObservedAt: time.Now().UTC()}
		next, err = copyCreationJournal(root.ActionJournal)
		if err != nil {
			return nil, err
		}
		next.ImportInverse.Results = append(next.ImportInverse.Results, *observed)
		root, err = c.writeImportInverseReceipt(ctx, root, next, func() error {
			current, err := c.observeImportInverse(ctx, root, true)
			if err == nil && (current == nil || !sameImportNativeState(c.repo, current.State, observed.State)) {
				return fmt.Errorf("import inverse result changed before receipt publication")
			}
			return err
		})
		if err != nil {
			return nil, err
		}
	}
	root, err = c.completeImportInverse(ctx, root, session, actor.Login, "verified")
	if err != nil {
		return nil, err
	}
	return importUndoResult(root, false)
}

// Called only on this executor's pre-PATCH return paths. Root publication still
// fences the exact native command; no target observation can authorize this
// attestation. Later undo repeats all ordinary target/graph conflict checks.
func (c *Client) cancelImportInverseBeforeWrite(ctx context.Context, root *ActionJournalRecord, session, actor, stage string, cause error) error {
	if root.Phase != "undo_prepared" || root.SessionID != session || !strings.EqualFold(root.Author, actor) || root.ImportInverse == nil || root.ImportInverse.Started != len(root.ImportInverse.Results)+1 {
		return errors.Join(cause, fmt.Errorf("import inverse cancellation requires its original executor and pending step"))
	}
	next, err := copyCreationJournal(root.ActionJournal)
	if err == nil {
		next.ImportInverse.Started--
		next.ImportInverse.Canceled = append(next.ImportInverse.Canceled, ImportInverseCancellation{
			Step: len(next.ImportInverse.Results), Stage: stage,
			Observation: CreationObservation{Proof: "verified", SessionID: session, Actor: actor, ObservedAt: time.Now().UTC()},
		})
		_, err = c.writeImportInverseReceipt(ctx, root, next, nil)
	}
	if err != nil {
		return errors.Join(cause, fmt.Errorf("import inverse %s PATCH was not attempted, but its cancellation could not be recorded; step remains uncertain: %w", root.ID, err))
	}
	return fmt.Errorf("import inverse %s PATCH was not attempted; cancellation recorded, original session may retry undo: %w", root.ID, cause)
}

func (c *Client) completeImportInverse(ctx context.Context, root *ActionJournalRecord, session, actor, proof string) (*ActionJournalRecord, error) {
	next, err := copyCreationJournal(root.ActionJournal)
	if err != nil {
		return nil, err
	}
	for _, result := range next.ImportInverse.Results {
		if result.Observation.Proof != "verified" {
			proof = "observed"
		}
	}
	next.Phase = "undone"
	next.ImportInverse.Result = &CreationObservation{Proof: proof, SessionID: session, Actor: actor, ObservedAt: time.Now().UTC()}
	return c.writeImportInverseReceipt(ctx, root, next, func() error {
		_, err := c.observeImportInverse(ctx, root, false)
		return err
	})
}

func (c *Client) reconcileImportInverse(ctx context.Context, root *ActionJournalRecord, session, actor string, apply bool) (*ActionReconcileResult, error) {
	observed, err := c.observeImportInverse(ctx, root, true)
	if err != nil {
		return nil, err
	}
	result := &ActionReconcileResult{Journal: root, AlreadyReconciled: root.Phase == "undone"}
	if result.AlreadyReconciled {
		return result, nil
	}
	result.Partial = len(root.ImportInverse.Results) < len(root.ImportInverse.Steps)
	if observed != nil && apply {
		observed.Observation = CreationObservation{Proof: "observed", SessionID: session, Actor: actor, ObservedAt: time.Now().UTC()}
		next, err := copyCreationJournal(root.ActionJournal)
		if err != nil {
			return nil, err
		}
		next.ImportInverse.Results = append(next.ImportInverse.Results, *observed)
		root, err = c.writeImportInverseReceipt(ctx, root, next, func() error {
			current, err := c.observeImportInverse(ctx, root, true)
			if err == nil && (current == nil || !sameImportNativeState(c.repo, current.State, observed.State)) {
				return fmt.Errorf("import inverse observation changed before receipt-only recovery")
			}
			return err
		})
		if err != nil {
			return nil, err
		}
		result.Applied, result.Journal = true, root
	}
	result.Partial = len(root.ImportInverse.Results) < len(root.ImportInverse.Steps)
	if !result.Partial && apply {
		result.Journal, err = c.completeImportInverse(ctx, root, session, actor, "observed")
		result.Applied = err == nil
	}
	return result, err
}

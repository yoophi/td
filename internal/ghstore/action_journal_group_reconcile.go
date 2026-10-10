package ghstore

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"
)

type groupObservation struct {
	Members   []ActionGroupResultMember
	Receipts  map[string]*ActionJournalRecord
	Records   map[string]Record
	Partial   bool
	Pending   bool
	Remaining bool
}

// Observe all task revisions and receipt membership from one complete fresh
// bulk snapshot. Native event proofs are read only when a member recorded them.
func (c *Client) observeActionGroup(ctx context.Context, a *ActionJournalRecord, normal bool) (*groupObservation, error) {
	snapshot, err := c.ReadSnapshot(WithFreshSnapshot(ctx), true)
	if err != nil {
		return nil, err
	}
	if err := verifyGraphGroupMembership(a.Group, snapshot.Issues(true)); err != nil {
		return nil, err
	}
	history, err := snapshot.ActionHistory("")
	if err != nil {
		return nil, err
	}
	out := &groupObservation{Members: []ActionGroupResultMember{}, Receipts: map[string]*ActionJournalRecord{}, Records: map[string]Record{}}
	carriers, found := 0, 0
	for _, record := range snapshot.records {
		if record.meta.EntityKind == "journal" {
			carriers++
			if record.ID != a.CarrierID {
				return nil, fmt.Errorf("group carrier is ambiguous")
			}
		}
		out.Records[record.ID] = record
	}
	attempts := []*ActionJournalRecord{}
	for i := range history {
		record := &history[i]
		if record.ID == a.ID {
			found++
			if !reflect.DeepEqual(*record, *a) {
				return nil, fmt.Errorf("group receipt changed during observation")
			}
		}
		if record.GroupOperationID == a.OperationID {
			attempts = append(attempts, record)
		}
	}
	chains, err := orderedGroupEffects(a, attempts)
	if err != nil {
		return nil, err
	}
	for id, chain := range chains {
		// v10/v12/v13 keep their original one-attempt-per-target contract.
		// Forward chain validation alone does not authorize inverse bridges.
		if len(chain) != 1 {
			return nil, fmt.Errorf("group member has duplicate attempts; inspect before retrying")
		}
		out.Receipts[id] = chain[0]
	}
	if carriers != 1 || found != 1 {
		return nil, fmt.Errorf("group needs one original carrier and its exact receipt")
	}
	inverse := a.Phase == "undo_prepared" || a.Phase == "undone"
	if inverse {
		out.Partial = a.Group.Forward.Partial
	}
	for i, m := range a.Group.Members {
		current, found := out.Records[m.IssueID]
		if !found {
			return nil, &ConflictError{ID: m.IssueID}
		}
		state, err := actionIssueState(&current)
		if err != nil {
			return nil, err
		}
		result := ActionGroupResultMember{IssueID: m.IssueID, State: state}
		receipt := out.Receipts[m.IssueID]
		if inverse {
			forward := a.Group.Forward.Members[i]
			if forward.Status == "noop" || forward.Status == "unattempted" || forward.Status == "unapplied" {
				if state.Revision != m.Before.Revision {
					return nil, &ConflictError{ID: m.IssueID}
				}
				if forward.ReceiptID != "" && (receipt == nil || receipt.ID != forward.ReceiptID || receipt.Phase != "prepared") {
					return nil, fmt.Errorf("unapplied group attempt changed after its observation")
				}
				if forward.ReceiptID == "" && receipt != nil {
					return nil, fmt.Errorf("unattempted group member acquired a later attempt")
				}
				result.Status, result.ReceiptID = forward.Status, forward.ReceiptID
			} else {
				if receipt == nil || receipt.ID != forward.ReceiptID || receipt.After == nil || receipt.After.Revision != forward.State.Revision {
					return nil, fmt.Errorf("group member replaced its original forward result")
				}
				result.ReceiptID = receipt.ID
				switch receipt.Phase {
				case "applied":
					if state.Revision != receipt.After.Revision {
						return nil, &ConflictError{ID: m.IssueID}
					}
					result.Status = "applied"
					out.Remaining = true
				case "undo_prepared":
					if err := groupMemberInverseObserved(ctx, c, receipt, &current, m.Before, true); err != nil {
						return nil, err
					}
					result.Status = "pending-undone"
					out.Pending = true
				case "undone":
					if receipt.Restored == nil || state.Revision != receipt.Restored.Revision {
						return nil, &ConflictError{ID: m.IssueID}
					}
					if err := groupMemberInverseObserved(ctx, c, receipt, &current, m.Before, false); err != nil {
						return nil, err
					}
					result.Status = "undone"
				default:
					return nil, fmt.Errorf("group member has an invalid inverse phase")
				}
			}
		} else if receipt == nil {
			if state.Revision != m.Before.Revision {
				return nil, &ConflictError{ID: m.IssueID}
			}
			result.Status = "unattempted"
			out.Partial = true
			if normal {
				result.Status = "noop"
				out.Partial = false
			}
			if a.Group.Forward != nil {
				result.Status = a.Group.Forward.Members[i].Status
				out.Partial = a.Group.Forward.Partial
			}
		} else {
			result.ReceiptID = receipt.ID
			switch receipt.Phase {
			case "prepared":
				if normal {
					return nil, fmt.Errorf("successful group callback has an unverified member %s", receipt.ID)
				}
				want, err := receipt.UpdateIntent.Target.contents()
				if err != nil {
					return nil, err
				}
				if sameActionContents(want, state) && !state.UpdatedAt.Before(m.Before.UpdatedAt) {
					result.Status = "pending-applied"
					out.Pending = true
				} else if state.Revision == m.Before.Revision {
					result.Status = "unapplied"
					out.Partial = true
				} else {
					return nil, &ConflictError{ID: m.IssueID}
				}
			case "applied":
				if receipt.After == nil || state.Revision != receipt.After.Revision {
					return nil, &ConflictError{ID: m.IssueID}
				}
				if receipt.UpdateIntent.NativeEvents != nil {
					events, err := c.readStateEvents(withReviewCost(ctx), m.IssueID)
					if err != nil {
						return nil, err
					}
					if err := updateEventProof(receipt.ActionJournal, events); err != nil {
						return nil, err
					}
				}
				result.Status = "applied"
				if receipt.UpdateObservation != nil {
					result.Status = "observed"
				}
			default:
				return nil, fmt.Errorf("group member acquired an inverse before the group intent")
			}
		}
		out.Members = append(out.Members, result)
	}
	// A saved result fences exact original source clocks and member identities.
	saved := a.Group.Forward
	if inverse {
		saved = a.Group.Inverse
	}
	if saved != nil && !equalGroupMemberResults(saved.Members, out.Members) {
		return nil, fmt.Errorf("group result or member revision changed after completion")
	}
	return out, nil
}

func sameGroupObservation(a, b *groupObservation) bool {
	return equalGroupMemberResults(a.Members, b.Members) && reflect.DeepEqual(a.Receipts, b.Receipts)
}

func (c *Client) writeGroupReceipt(ctx context.Context, a *ActionJournalRecord, next ActionJournal, fence func() error) (*ActionJournalRecord, error) {
	if err := validateGroupRewrite(a.ActionJournal, next); err != nil {
		return nil, err
	}
	if a.OperationID != next.OperationID || a.SessionID != next.SessionID || a.IssueID != next.IssueID || a.CarrierID != next.CarrierID || a.Action != next.Action {
		return nil, fmt.Errorf("group identity is immutable")
	}
	valid := (a.Phase == "prepared" && next.Phase == "applied") || (a.Phase == "applied" && next.Phase == "undo_prepared") || (a.Phase == "undo_prepared" && next.Phase == "undone" && a.UndoOperationID == next.UndoOperationID)
	if !valid {
		return nil, fmt.Errorf("invalid group phase transition")
	}
	oldBody, err := renderExistingActionJournal(a)
	if err != nil {
		return nil, err
	}
	body, err := renderWriteActionJournal(next)
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
		return nil, fmt.Errorf("group receipt changed before write")
	}
	if fence != nil {
		if err := fence(); err != nil {
			return nil, err
		}
	}
	written, err := c.journalComment(ctx, path, map[string]string{"body": body}, body, a.CarrierID)
	if err != nil {
		return nil, fmt.Errorf("group receipt unverified; inspect before retrying, no target replay: %w", err)
	}
	verified, err := c.journalComment(withReadbackCost(ctx), path, nil, body, a.CarrierID)
	if err != nil {
		return nil, err
	}
	if !sameJournalIdentity(a, verified) || !reflect.DeepEqual(written, verified) {
		return nil, fmt.Errorf("group receipt identity changed during readback")
	}
	return verified, nil
}

func (c *Client) reconcileActionGroup(ctx context.Context, a *ActionJournalRecord, session, actor string, apply, normal bool) (*ActionReconcileResult, error) {
	if a.Group != nil && a.Group.Ordered != nil {
		return nil, &UndoScopeError{Reason: "ordered group recovery integration is incomplete; no target or receipt write attempted"}
	}
	if a.Group == nil || !strings.EqualFold(a.Author, actor) {
		return nil, fmt.Errorf("group recovery requires its native author")
	}
	observed, err := c.observeActionGroup(ctx, a, normal)
	if err != nil {
		return nil, err
	}
	result := &ActionReconcileResult{Journal: a, GroupMembers: observed.Members, TargetWriteReplayed: false}
	root := observed.Records[a.IssueID]
	result.Issue = &root
	inverse := a.Phase == "undo_prepared" || a.Phase == "undone"
	outcome := groupResultOutcome(a, inverse)
	if inverse {
		result.UndoOutcome = outcome
	} else {
		result.ForwardOutcome = outcome
	}
	result.AlreadyReconciled = a.Phase == "applied" || a.Phase == "undone"
	if result.AlreadyReconciled || !apply {
		return result, nil
	}
	if observed.Pending {
		// All source membership is fenced before any receipt-only member recovery.
		fresh, err := c.observeActionGroup(ctx, a, normal)
		if err != nil {
			return nil, err
		}
		if !sameGroupObservation(observed, fresh) {
			return nil, fmt.Errorf("group observation changed before member receipt recovery")
		}
		for _, m := range observed.Members {
			if m.Status != "pending-applied" && m.Status != "pending-undone" {
				continue
			}
			if _, err := c.ReconcileAction(ctx, m.ReceiptID, session, true); err != nil {
				return nil, fmt.Errorf("group member %s receipt recovery stopped; earlier receipt changes remain, no target replay: %w", m.ReceiptID, err)
			}
			result.Applied = true
		}
		observed, err = c.observeActionGroup(ctx, a, normal)
		if err != nil {
			return nil, err
		}
		result.GroupMembers = observed.Members
	}
	if inverse && observed.Remaining {
		return result, nil
	}
	observation := &ActionGroupObservation{SessionID: session, Actor: actor, ObservedAt: time.Now().UTC(), Proof: "group-contents-observed", Partial: observed.Partial, Members: observed.Members}
	if normal {
		observation.Proof = "verified-group-receipts"
	}
	if inverse && normal {
		for _, m := range observed.Members {
			if m.Status == "undone" && nativeGroupMemberProofObserved(observed.Receipts[m.IssueID]) {
				observation.Proof = "group-contents-observed"
			}
		}
	}
	group := *a.Group
	next := a.ActionJournal
	next.Group = &group
	if inverse {
		group.Inverse = observation
		next.Phase = "undone"
	} else {
		group.Forward = observation
		next.Phase = "applied"
	}
	written, err := c.writeGroupReceipt(ctx, a, next, func() error {
		fresh, err := c.observeActionGroup(ctx, a, normal)
		if err != nil {
			return err
		}
		if !sameGroupObservation(observed, fresh) {
			return fmt.Errorf("group changed at receipt fence")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if _, err := c.observeActionGroup(ctx, written, normal); err != nil {
		return nil, fmt.Errorf("group receipt saved but final observation failed: %w", err)
	}
	result.Journal, result.Applied, result.GroupReceiptSaved = written, true, true
	return result, nil
}

func groupResultOutcome(a *ActionJournalRecord, inverse bool) *ActionUndoOutcome {
	observation := a.Group.Forward
	if inverse {
		observation = a.Group.Inverse
	}
	if observation == nil {
		return &ActionUndoOutcome{Proof: "group-contents-observed"}
	}
	outcome := &ActionUndoOutcome{Proof: observation.Proof}
	if observation.Proof == "verified-group-receipts" {
		for _, m := range observation.Members {
			if m.Status == "applied" || m.Status == "undone" {
				outcome.TargetWriteVerified = true
			}
		}
	}
	return outcome
}

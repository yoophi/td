package ghstore

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"
)

const actionCommentObservedJournalStart = actionJournalPrefix + "v18\n"
const commentObservationProof = "comment-contents-observed"

// The original intent and native comment remain distinct from the later
// observer. Matching contents do not prove who executed an uncertain POST.
type ActionCommentObservation struct {
	SessionID  string            `json:"session_id"`
	Actor      string            `json:"actor"`
	ObservedAt time.Time         `json:"observed_at"`
	Proof      string            `json:"proof"`
	State      *ActionIssueState `json:"state"`
}

func (a ActionJournal) validateCommentObservation() error {
	o := a.CommentObservation
	if o == nil {
		return nil
	}
	if a.Action != "comment_create" || a.Phase == "prepared" || a.Before == nil || a.After == nil || a.CommentAfter == nil ||
		a.UpdateObservation != nil || a.UpdateIntent != nil || a.Reconciliation != nil || a.NativeUndo != nil ||
		o.Actor == "" || o.ObservedAt.IsZero() || o.Proof != commentObservationProof || o.State == nil || !reflect.DeepEqual(o.State, a.After) {
		return fmt.Errorf("comment observation requires its original input and observed native result")
	}
	probe := ActionJournal{OperationID: "probe", SessionID: o.SessionID, IssueID: "gh-1", Action: "create", Phase: "prepared"}
	return probe.validate()
}

// One bulk snapshot proves the original carrier/receipt, unique activity
// operation and unchanged task contents. Point reads then fence both mutable
// native objects. This observation never creates, edits or deletes an activity.
func (c *Client) observeForwardComment(ctx context.Context, a *ActionJournalRecord) (*Record, *ActionCommentState, error) {
	if a.Action != "comment_create" || a.CommentInput == nil || a.Before == nil || (a.Phase != "prepared" && a.Phase != "applied") {
		return nil, nil, fmt.Errorf("comment recovery needs its original creation intent; no target replay")
	}
	snapshot, err := c.ReadSnapshot(WithFreshSnapshot(ctx), true)
	if err != nil {
		return nil, nil, err
	}
	history, err := snapshot.ActionHistory("")
	if err != nil {
		return nil, nil, err
	}
	carriers, receipts := 0, 0
	var current *Record
	for i, record := range snapshot.records {
		if record.meta.EntityKind == "journal" {
			carriers++
			if record.ID != a.CarrierID {
				return nil, nil, fmt.Errorf("comment recovery carrier is ambiguous")
			}
		}
		if record.ID == a.IssueID {
			current = &snapshot.records[i]
		}
	}
	for _, receipt := range history {
		if receipt.ID == a.ID {
			receipts++
			if !reflect.DeepEqual(receipt, *a) {
				return nil, nil, fmt.Errorf("comment recovery receipt changed during observation")
			}
		}
	}
	if carriers != 1 || receipts != 1 || current == nil {
		return nil, nil, fmt.Errorf("comment recovery requires one carrier, its exact receipt and the native target")
	}
	state, err := actionIssueState(current)
	if err != nil {
		return nil, nil, err
	}
	if !sameActionContents(*a.Before, state) || state.UpdatedAt.Before(a.Before.UpdatedAt) || a.After != nil && state.Revision != a.After.Revision {
		return nil, nil, &ConflictError{ID: a.IssueID}
	}
	found, err := c.commentCreationInSnapshot(snapshot, a, state)
	if err != nil {
		return nil, nil, err
	}
	if found == nil {
		return nil, nil, fmt.Errorf("comment creation result is absent from the fresh collection; inspect before retrying, no target replay")
	}
	if a.CommentAfter != nil && !reflect.DeepEqual(a.CommentAfter, found) {
		return nil, nil, fmt.Errorf("comment recovery result changed since completion")
	}
	if err := c.readNativeComment(withReadbackCost(ctx), found); err != nil {
		return nil, nil, err
	}
	point, err := c.readJournalTarget(withReadbackCost(ctx), a.IssueID)
	if err != nil {
		return nil, nil, err
	}
	if point.revision != current.revision {
		return nil, nil, &ConflictError{ID: a.IssueID}
	}
	return point, found, nil
}

func (c *Client) commentCreationInSnapshot(snapshot *Snapshot, a *ActionJournalRecord, state ActionIssueState) (*ActionCommentState, error) {
	var found *ActionCommentState
	for _, entry := range snapshot.entries {
		if fmt.Sprintf("gh-%d", entry.Issue.Number) != a.IssueID {
			continue
		}
		for _, comment := range entry.Comments {
			activity, err := decodeActivity(comment, a.IssueID)
			if err != nil {
				return nil, err
			}
			if activity.Kind == "action_journal" || activity.OperationID != a.commentOperationID() {
				continue
			}
			if found != nil {
				return nil, fmt.Errorf("comment recovery found duplicate activity operation %s; no target replay", a.commentOperationID())
			}
			found, err = c.nativeCommentState(comment, a.IssueID, a.CommentInput.Body)
			if err != nil {
				return nil, err
			}
			intentID, _ := CommentNumber(a.ID)
			if comment.ID <= intentID || found.CreatedAt.Before(a.CreatedAt) || !strings.EqualFold(found.Author, a.Author) || !found.UpdatedAt.Equal(found.CreatedAt) || state.UpdatedAt.Before(found.UpdatedAt) {
				return nil, fmt.Errorf("comment recovery result has different attribution, predates intent or was edited")
			}
		}
	}
	if a.CommentAfter != nil && !reflect.DeepEqual(a.CommentAfter, found) {
		return nil, fmt.Errorf("activity result changed or disappeared")
	}
	return found, nil
}

func (c *Client) reconcileForwardComment(ctx context.Context, a *ActionJournalRecord, session, actor string, apply bool) (*ActionReconcileResult, error) {
	current, comment, err := c.observeForwardComment(ctx, a)
	if err != nil {
		return nil, err
	}
	outcome := &ActionUndoOutcome{Proof: commentObservationProof}
	if a.Phase == "applied" && a.CommentObservation == nil {
		outcome = &ActionUndoOutcome{Proof: "verified-forward-receipt", TargetWriteVerified: true, CausalUndoChainEligible: true}
	}
	result := &ActionReconcileResult{Issue: current, Journal: a, AlreadyReconciled: a.Phase == "applied", ForwardOutcome: outcome}
	if result.AlreadyReconciled || !apply {
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
		return nil, fmt.Errorf("comment recovery original receipt changed; no target replay")
	}
	checked, checkedComment, err := c.observeForwardComment(ctx, a)
	if err != nil {
		return nil, err
	}
	if checked.revision != current.revision || !reflect.DeepEqual(checkedComment, comment) {
		return nil, fmt.Errorf("comment recovery evidence changed before receipt; no target replay")
	}
	state, err := actionIssueState(current)
	if err != nil {
		return nil, err
	}
	resolved := a.ActionJournal
	resolved.Phase, resolved.After, resolved.CommentAfter = "applied", &state, comment
	resolved.CommentObservation = &ActionCommentObservation{SessionID: session, Actor: actor, ObservedAt: time.Now().UTC(), Proof: commentObservationProof, State: &state}
	body, err := renderWriteActionJournal(resolved)
	if err != nil {
		return nil, err
	}
	written, err := c.journalComment(ctx, path, map[string]string{"body": body}, body, a.CarrierID)
	if err != nil {
		return nil, fmt.Errorf("comment recovery receipt is unverified; inspect before retrying, no target replay: %w", err)
	}
	if !sameJournalIdentity(a, written) || !strings.EqualFold(written.Author, actor) {
		return nil, fmt.Errorf("comment recovery receipt identity changed; no target replay")
	}
	verified, err := c.journalComment(withReadbackCost(ctx), path, nil, body, a.CarrierID)
	if err != nil {
		return nil, fmt.Errorf("comment recovery receipt readback is unverified; no target replay: %w", err)
	}
	if !reflect.DeepEqual(written, verified) {
		return nil, fmt.Errorf("comment recovery receipt changed during readback")
	}
	if _, _, err := c.observeForwardComment(ctx, verified); err != nil {
		return nil, fmt.Errorf("comment recovery receipt saved but final observation failed; no target replay: %w", err)
	}
	result.Journal, result.Applied = verified, true
	return result, nil
}

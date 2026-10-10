package ghstore

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/reviewpolicy"
	"github.com/marcus/td/internal/workflow"
)

// NativeIssueUndoPlan is a compensation, not a historical event erasure. A
// guarded executor must separately verify native events and persist its intent
// before using this payload. Constructing the plan performs no remote writes.
type NativeIssueUndoPlan struct {
	Payload                       map[string]any `json:"payload"`
	UndoOperationID               string         `json:"undo_operation_id"`
	OriginalTransitionOperationID string         `json:"original_transition_operation_id"`
	SessionID                     string         `json:"session_id"`
	Actor                         string         `json:"actor"`
	At                            time.Time      `json:"at"`
	ForwardNativeEvent            string         `json:"forward_native_event"`
	InverseNativeEvent            string         `json:"inverse_native_event"`
	RequiresNewReview             bool           `json:"requires_new_review"`
	ExactHistoricalRestore        bool           `json:"exact_historical_restore"`
	CausalUndoChainEligible       bool           `json:"causal_undo_chain_eligible"`
}

// Recognize every single-target native writer effect. Coordinated cascades and
// unrelated edits are excluded rather than silently absorbed by compensation.
func nativeUndoScope(action *ActionJournalRecord) (TransitionRecord, bool, error) {
	fail := func() (TransitionRecord, bool, error) { return TransitionRecord{}, false, nil }
	if action.Action != "update" || action.Before == nil || action.After == nil || action.entityKind() != "" || action.Before.State == action.After.State {
		return fail()
	}
	bdesc, bm, _, err := decodeBody(action.Before.Body)
	if err != nil {
		return TransitionRecord{}, false, err
	}
	adesc, am, _, err := decodeBody(action.After.Body)
	if err != nil {
		return TransitionRecord{}, false, err
	}
	bd, ad := IssueDetails{Status: models.Status(action.Before.State)}, IssueDetails{Status: models.Status(action.After.State)}
	if bm.Details != nil {
		bd = *bm.Details
	}
	if am.Details != nil {
		ad = *am.Details
	}
	if bd.Status == "" {
		bd.Status = models.Status(action.Before.State)
	}
	if ad.Status == "" {
		ad.Status = models.Status(action.After.State)
	}
	if bd.DeletedAt != nil || ad.DeletedAt != nil || string(nativeStatus(bd.Status)) != action.Before.State || string(nativeStatus(ad.Status)) != action.After.State || !workflow.DefaultMachine().IsValidTransition(bd.Status, ad.Status) {
		return fail()
	}
	if len(ad.Transitions) != len(bd.Transitions)+1 || !slices.EqualFunc(bd.Transitions, ad.Transitions[:len(bd.Transitions)], func(a, b TransitionRecord) bool { return reflect.DeepEqual(a, b) }) {
		return fail()
	}
	tr := ad.Transitions[len(bd.Transitions)]
	if tr.SessionID != action.SessionID || tr.OperationID == "" || tr.At.IsZero() || tr.From != bd.Status || tr.To != ad.Status || tr.RelatedIssueID != "" || tr.Snapshot != nil {
		return fail()
	}
	for _, old := range bd.Transitions {
		if old.OperationID == tr.OperationID {
			return fail()
		}
	}
	expected := bd
	delta := []models.IssueSessionAction{}
	reusedApproval := activeApproval(&bd) != nil && bd.Status == models.StatusInReview && len(ad.Reviews) == len(bd.Reviews)
	switch tr.Action {
	case "close", "approve":
		if ad.Status != models.StatusClosed {
			return fail()
		}
		if tr.Action == "approve" && (bd.Status != models.StatusInReview || tr.AdminReason != "" || tr.SelfCloseException != "") {
			return fail()
		}
		if reusedApproval {
			// Closing an existing approved checkpoint adds only a close participation.
		} else if tr.Action == "approve" {
			if len(ad.Reviews) != len(bd.Reviews)+1 || bd.ReviewBasis == "" {
				return fail()
			}
			oldAfter := ad
			oldAfter.Reviews = ad.Reviews[:len(bd.Reviews)]
			var valid bool
			expected, valid = revokedReviewDetails(bd, oldAfter)
			if !valid {
				return fail()
			}
			expected.ReviewBasis = bd.ReviewBasis
			row := ad.Reviews[len(bd.Reviews)]
			if row.ID == "" || row.IssueID != action.IssueID || row.ReviewerSession != action.SessionID || row.Decision != reviewpolicy.DecisionApproved || row.Summary != tr.Reason || row.RequestedBySession != bd.ReviewRequestedBySession || !row.CreatedAt.Equal(tr.At) || row.SupersededAt != nil {
				return fail()
			}
			for _, old := range bd.Reviews {
				if old.ID == row.ID {
					return fail()
				}
			}
			expected.Reviews = ad.Reviews
			expected.ReviewerSession = action.SessionID
			expected.ReviewedAt = &tr.At
			delta = append(delta, models.ActionSessionReviewApproved)
		} else {
			var valid bool
			expected, valid = revokedReviewDetails(bd, ad)
			if !valid {
				return fail()
			}
		}
		expected.ClosedBySession = action.SessionID
		delta = append(delta, models.ActionSessionClosed)
	case "reopen":
		if bd.Status != models.StatusClosed || ad.Status != models.StatusOpen || tr.AdminReason != "" || tr.SelfCloseException != "" {
			return fail()
		}
		var valid bool
		expected, valid = revokedReviewDetails(bd, ad)
		if !valid {
			return fail()
		}
		expected.ImplementerSession, expected.ReviewRequestedBySession, expected.ClosedBySession = "", "", ""
	default:
		return fail()
	}
	if !reusedApproval {
		for i, row := range bd.Reviews {
			if row.SupersededAt == nil && !ad.Reviews[i].SupersededAt.Equal(tr.At) {
				return fail()
			}
		}
	}
	if len(ad.Sessions) != len(bd.Sessions)+len(delta) || !slices.EqualFunc(bd.Sessions, ad.Sessions[:len(bd.Sessions)], func(a, b models.IssueSessionHistory) bool { return reflect.DeepEqual(a, b) }) {
		return fail()
	}
	ids := map[string]bool{}
	for _, row := range bd.Sessions {
		ids[row.ID] = true
	}
	for i, want := range delta {
		row := ad.Sessions[len(bd.Sessions)+i]
		if row.ID == "" || ids[row.ID] || row.IssueID != action.IssueID || row.SessionID != action.SessionID || row.Action != want || !row.CreatedAt.Equal(tr.At) {
			return fail()
		}
		ids[row.ID] = true
	}
	expected.Status = ad.Status
	expected.Sessions, expected.Transitions = ad.Sessions, ad.Transitions
	if !reflect.DeepEqual(expected, ad) {
		return fail()
	}
	bm.Details, am.Details = nil, nil
	bm.LastStateReason = tr.Reason
	labels := mirroredPriorityLabels(mirroredLabels(action.Before.Labels, ad.Status), am.Priority)
	actual := slices.Clone(action.After.Labels)
	slices.Sort(labels)
	slices.Sort(actual)
	if !reflect.DeepEqual(bm, am) || bdesc != adesc || action.Before.Title != action.After.Title || !slices.Equal(labels, actual) {
		return fail()
	}
	return tr, true, nil
}

// PlanNativeIssueUndo validates current attribution/revision and keeps all
// forward audit rows. It restores the former task fields while superseding
// current reviews; the original snapshots are never modified. The execution
// path must obtain native-event evidence before persisting or applying it.
func (a ActionJournalRecord) PlanNativeIssueUndo(current *Record, session, actor, undoOperation string, at time.Time) (*NativeIssueUndoPlan, error) {
	tr, supported, err := nativeUndoScope(&a)
	if err != nil {
		return nil, err
	}
	if !supported {
		return nil, &UndoScopeError{Reason: "native compensation requires one verified close, approve or reopen writer effect"}
	}
	if a.Phase != "applied" {
		return nil, fmt.Errorf("native compensation requires a verified applied action; inspect uncertain outcomes without replay")
	}
	if _, err := a.PlanIssueUndo(current, session, actor); err != nil {
		return nil, err
	}
	probe := ActionJournal{OperationID: undoOperation, SessionID: session, IssueID: a.IssueID, Action: "create", Phase: "prepared"}
	if err := probe.validate(); err != nil || undoOperation == a.OperationID || at.IsZero() || at.Before(tr.At) || at.Before(a.After.UpdatedAt) {
		return nil, fmt.Errorf("native compensation requires a distinct valid undo identity and non-reversing time")
	}
	_, priorMeta, _, _ := decodeBody(a.After.Body)
	if priorMeta.Details != nil {
		for _, old := range priorMeta.Details.Transitions {
			if old.OperationID == undoOperation {
				return nil, fmt.Errorf("native compensation cannot reuse a prior transition identity")
			}
		}
	}
	desc, bm, _, _ := decodeBody(a.Before.Body)
	_, am, _, _ := decodeBody(a.After.Body)
	// Copy through JSON so changing review supersession cannot alter the journal.
	data, err := json.Marshal(am.Details)
	if err != nil {
		return nil, err
	}
	var audit IssueDetails
	if err := json.Unmarshal(data, &audit); err != nil {
		return nil, err
	}
	d := IssueDetails{Status: models.Status(a.Before.State)}
	if bm.Details != nil {
		d = *bm.Details
	}
	requires := !d.Minor && (d.ReviewBasis != "" || audit.ReviewBasis != "" || activeApproval(&d) != nil || activeApproval(&audit) != nil)
	d.Reviews, d.Sessions, d.Transitions = audit.Reviews, audit.Sessions, audit.Transitions
	supersedeReviews(&d, at)
	d.ReviewBasis, d.ReviewEvents, d.ReviewRequestedBySession = "", "", ""
	d.ReviewHandoffID = ""
	d.ReviewHandoffUpdatedAt = time.Time{}
	d.ClosedBySession = ""
	if a.Before.State == "closed" {
		d.ClosedBySession = session
	}
	reason := "Native undo compensation for " + tr.OperationID + "; native events retained and previous approvals invalidated"
	d.Transitions = append(d.Transitions, TransitionRecord{OperationID: undoOperation, Action: "undo_native", From: nativeUndoStatus(am, a.After.State), To: d.Status, SessionID: session, Reason: reason, At: at})
	bm.Details = &d
	bm.LastStateReason = reason
	body, err := encodeBody(desc, bm)
	if err != nil {
		return nil, err
	}
	forward, inverse := "closed", "reopened"
	if a.After.State == "open" {
		forward, inverse = "reopened", "closed"
	}
	return &NativeIssueUndoPlan{Payload: map[string]any{"title": a.Before.Title, "body": body, "state": a.Before.State, "labels": slices.Clone(a.Before.Labels)}, UndoOperationID: undoOperation, OriginalTransitionOperationID: tr.OperationID, SessionID: session, Actor: actor, At: at, ForwardNativeEvent: forward, InverseNativeEvent: inverse, RequiresNewReview: requires}, nil
}

func nativeUndoStatus(meta metadata, native string) models.Status {
	if meta.Details != nil && meta.Details.Status != "" {
		return meta.Details.Status
	}
	return models.Status(native)
}

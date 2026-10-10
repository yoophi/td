package ghstore

import (
	"encoding/hex"
	"encoding/json"
	"reflect"
	"slices"
	"strings"

	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/reviewpolicy"
	"github.com/marcus/td/internal/workflow"
)

// Open-native review inverses remove a newly requested/recorded review or
// restore the original review after rejection. They never invent an approval
// or erase native close/reopen events.
func reviewTransitionUndoScope(action *ActionJournalRecord) (bool, error) {
	if action.Action != "update" || action.Before == nil || action.After == nil || action.entityKind() != "" || action.Before.State != "open" || action.After.State != "open" {
		return false, nil
	}
	bdesc, bm, _, err := decodeBody(action.Before.Body)
	if err != nil {
		return false, err
	}
	adesc, am, _, err := decodeBody(action.After.Body)
	if err != nil {
		return false, err
	}
	bd, ad := IssueDetails{Status: models.StatusOpen}, IssueDetails{Status: models.StatusOpen}
	if bm.Details != nil {
		bd = *bm.Details
	}
	if am.Details != nil {
		ad = *am.Details
	}
	if bd.Status == "" {
		bd.Status = models.StatusOpen
	}
	if ad.Status == "" {
		ad.Status = models.StatusOpen
	}
	if bd.DeletedAt != nil || ad.DeletedAt != nil || nativeStatus(bd.Status) != models.StatusOpen || nativeStatus(ad.Status) != models.StatusOpen {
		return false, nil
	}
	if len(ad.Transitions) != len(bd.Transitions)+1 || !slices.EqualFunc(ad.Transitions[:len(bd.Transitions)], bd.Transitions, func(a, b TransitionRecord) bool { return reflect.DeepEqual(a, b) }) {
		return false, nil
	}
	tr := ad.Transitions[len(bd.Transitions)]
	if tr.SessionID != action.SessionID || tr.OperationID == "" || tr.At.IsZero() || tr.From != bd.Status || tr.To != ad.Status || tr.Snapshot != nil || tr.RelatedIssueID != "" || tr.AdminReason != "" || tr.SelfCloseException != "" {
		return false, nil
	}
	for _, old := range bd.Transitions {
		if old.OperationID == tr.OperationID {
			return false, nil
		}
	}
	if bd.Status != ad.Status && !workflow.DefaultMachine().IsValidTransition(bd.Status, ad.Status) {
		return false, nil
	}
	rows := 0
	switch tr.Action {
	case "approve", "reject":
		rows = 1
	case "review":
	default:
		return false, nil
	}
	if len(ad.Reviews) != len(bd.Reviews)+rows {
		return false, nil
	}
	oldAfter := ad
	oldAfter.Reviews = ad.Reviews[:len(bd.Reviews)]
	expected, valid := revokedReviewDetails(bd, oldAfter)
	if !valid {
		return false, nil
	}
	// supersedeReviews clears pointers but preserves the existing checkpoint.
	expected.ReviewBasis = bd.ReviewBasis
	for i, b := range bd.Reviews {
		if b.SupersededAt == nil && !ad.Reviews[i].SupersededAt.Equal(tr.At) {
			return false, nil
		}
	}
	expected.Status = ad.Status
	delta := []models.IssueSessionAction{}
	if tr.Action == "review" {
		if ad.Status != models.StatusInReview || bd.Status == models.StatusInReview || bd.Minor && !ad.Minor {
			return false, nil
		}
		expected.Minor = ad.Minor
		if bd.ImplementerSession == "" {
			expected.ImplementerSession = action.SessionID
			delta = append(delta, models.ActionSessionStarted)
		}
		expected.ReviewRequestedBySession = action.SessionID
		expected.ClosedBySession = ""
		expected.ReviewHandoffID, expected.ReviewHandoffUpdatedAt = ad.ReviewHandoffID, ad.ReviewHandoffUpdatedAt
		if ad.ReviewHandoffID == "" {
			if !ad.ReviewHandoffUpdatedAt.IsZero() {
				return false, nil
			}
		} else {
			n, err := CommentNumber(ad.ReviewHandoffID)
			if err != nil || n <= 0 || ad.Minor || ad.ReviewHandoffUpdatedAt.IsZero() {
				return false, nil
			}
		}
		native := apiIssue{Number: 1, Title: action.Before.Title, Body: action.Before.Body, State: "open", UpdatedAt: action.Before.UpdatedAt}
		native.Number, err = Number(action.IssueID)
		if err != nil {
			return false, err
		}
		data, err := json.Marshal(native)
		if err != nil {
			return false, err
		}
		original, err := decodeIssue(data)
		if err != nil {
			return false, err
		}
		expected.ReviewBasis = reviewBasis(original, expected)
		digest, err := hex.DecodeString(ad.ReviewEvents)
		if err != nil || len(digest) != 32 {
			return false, nil
		}
		expected.ReviewEvents = ad.ReviewEvents
	} else {
		if bd.Status != models.StatusInReview {
			return false, nil
		}
		row := ad.Reviews[len(bd.Reviews)]
		if row.ID == "" || row.IssueID != action.IssueID || row.ReviewerSession != action.SessionID || row.Summary != tr.Reason || !row.CreatedAt.Equal(tr.At) || row.SupersededAt != nil {
			return false, nil
		}
		for _, old := range bd.Reviews {
			if old.ID == row.ID {
				return false, nil
			}
		}
		switch tr.Action {
		case "approve":
			if ad.Status != models.StatusInReview || bd.Minor || bd.ReviewBasis == "" || row.RequestedBySession != bd.ReviewRequestedBySession || strings.TrimSpace(row.Summary) == "" {
				return false, nil
			}
			switch row.Decision {
			case reviewpolicy.DecisionApproved:
				expected.ReviewerSession = action.SessionID
				expected.ReviewedAt = &tr.At
				delta = append(delta, models.ActionSessionReviewApproved)
			case reviewpolicy.DecisionChangesRequested:
				delta = append(delta, models.ActionSessionReviewChangesRequested)
			default:
				return false, nil
			}
		case "reject":
			if ad.Status != models.StatusOpen || row.Decision != reviewpolicy.DecisionChangesRequested || row.RequestedBySession != "" || row.SelfReview || row.ReviewedBy != "" {
				return false, nil
			}
			expected.ImplementerSession, expected.ReviewRequestedBySession, expected.ClosedBySession, expected.ReviewBasis = "", "", "", ""
			delta = append(delta, models.ActionSessionReviewChangesRequested)
		}
	}
	if len(ad.Sessions) != len(bd.Sessions)+len(delta) || !slices.EqualFunc(ad.Sessions[:len(bd.Sessions)], bd.Sessions, func(a, b models.IssueSessionHistory) bool { return reflect.DeepEqual(a, b) }) {
		return false, nil
	}
	ids := map[string]bool{}
	for _, old := range bd.Sessions {
		ids[old.ID] = true
	}
	for i, want := range delta {
		row := ad.Sessions[len(bd.Sessions)+i]
		if row.ID == "" || ids[row.ID] || row.IssueID != action.IssueID || row.SessionID != action.SessionID || row.Action != want || !row.CreatedAt.Equal(tr.At) {
			return false, nil
		}
		ids[row.ID] = true
	}
	expected.Reviews, expected.Sessions, expected.Transitions = ad.Reviews, ad.Sessions, ad.Transitions
	if !reflect.DeepEqual(expected, ad) {
		return false, nil
	}
	bm.Details, am.Details = nil, nil
	bm.LastStateReason = tr.Reason
	labels := mirroredPriorityLabels(mirroredLabels(action.Before.Labels, ad.Status), am.Priority)
	afterLabels := slices.Clone(action.After.Labels)
	slices.Sort(labels)
	slices.Sort(afterLabels)
	return reflect.DeepEqual(bm, am) && bdesc == adesc && action.Before.Title == action.After.Title && slices.Equal(labels, afterLabels), nil
}

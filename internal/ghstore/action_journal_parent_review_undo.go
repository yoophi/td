package ghstore

import (
	"encoding/hex"
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/marcus/td/internal/models"
)

// An aggregate epic request preserves its implementer and creates no approval.
// It is a different complete writer effect from an ordinary review request.
func autoParentReviewUndoScope(a *ActionJournalRecord) (bool, error) {
	if a.Action != "update" || a.Before == nil || a.After == nil || a.Before.State != "open" || a.After.State != "open" || a.entityKind() != "" {
		return false, nil
	}
	before, err := actionStateRecord(a.Before, a.IssueID)
	if err != nil {
		return false, err
	}
	after, err := actionStateRecord(a.After, a.IssueID)
	if err != nil {
		return false, err
	}
	if before.Type != models.TypeEpic || before.Status == models.StatusInReview || before.DeletedAt != nil || after.DeletedAt != nil || after.Status != models.StatusInReview {
		return false, nil
	}
	bd, err := before.CopyDetails()
	if err != nil {
		return false, err
	}
	ad, err := after.CopyDetails()
	if err != nil {
		return false, err
	}
	if len(ad.Transitions) != len(bd.Transitions)+1 || !slices.EqualFunc(ad.Transitions[:len(bd.Transitions)], bd.Transitions, func(x, y TransitionRecord) bool { return reflect.DeepEqual(x, y) }) {
		return false, nil
	}
	tr := ad.Transitions[len(bd.Transitions)]
	reason := "Auto-cascaded to in_review (all children complete)"
	if tr.Action != "review" || tr.OperationID == "" || tr.SessionID != a.SessionID || tr.At.IsZero() || tr.From != before.Status || tr.To != models.StatusInReview || tr.Reason != reason || tr.Snapshot != nil || tr.RelatedIssueID != "" || tr.AdminReason != "" || tr.SelfCloseException != "" {
		return false, nil
	}
	for _, old := range bd.Transitions {
		if old.OperationID == tr.OperationID {
			return false, nil
		}
	}
	digest, err := hex.DecodeString(ad.ReviewEvents)
	if err != nil || len(digest) != 32 {
		return false, nil
	}
	expected := bd
	supersedeReviews(&expected, tr.At)
	expected.Status = models.StatusInReview
	expected.ImplementerSession = before.ImplementerSession
	expected.ReviewHandoffID = ""
	expected.ReviewHandoffUpdatedAt = time.Time{}
	expected.ReviewBasis = ""
	expected.ReviewEvents = ad.ReviewEvents
	expected.ReviewRequestedBySession = a.SessionID
	expected.ClosedBySession = ""
	expected.ReviewBasis = reviewBasis(before, expected)
	expected.Transitions = append(expected.Transitions, tr)
	if !reflect.DeepEqual(expected, ad) {
		return false, nil
	}
	bdesc, bm, _, err := decodeBody(a.Before.Body)
	if err != nil {
		return false, err
	}
	adesc, am, _, err := decodeBody(a.After.Body)
	if err != nil {
		return false, err
	}
	bm.Details = nil
	am.Details = nil
	bm.LastStateReason = reason
	labels := mirroredPriorityLabels(mirroredLabels(a.Before.Labels, models.StatusInReview), am.Priority)
	slices.Sort(labels)
	other := slices.Clone(a.After.Labels)
	slices.Sort(other)
	return bdesc == adesc && a.Before.Title == a.After.Title && reflect.DeepEqual(bm, am) && slices.Equal(labels, other), nil
}

func reviewGroupMemberScope(g *ActionGroup, a *ActionJournalRecord) (bool, error) {
	matched, err := reviewTransitionUndoScope(a)
	if err != nil || matched {
		return matched, err
	}
	matched, err = autoParentReviewUndoScope(a)
	if err != nil || !matched {
		return matched, err
	}
	// Only a declared ancestor may use aggregate epic semantics.
	members := map[string]ActionGroupMember{}
	for _, m := range g.Members {
		members[m.IssueID] = m
	}
	for _, root := range g.Roots {
		id := root
		seen := map[string]bool{}
		for id != "" && !seen[id] {
			seen[id] = true
			m, ok := members[id]
			if !ok {
				break
			}
			r, err := actionStateRecord(&m.Before, id)
			if err != nil {
				return false, err
			}
			id = r.ParentID
			if id == a.IssueID {
				return true, nil
			}
		}
	}
	return false, fmt.Errorf("aggregate review member is not an original root ancestor")
}

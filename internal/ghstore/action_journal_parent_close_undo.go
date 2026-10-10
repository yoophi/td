package ghstore

import (
	"reflect"
	"slices"
	"time"

	"github.com/marcus/td/internal/models"
)

// Aggregate completion is an observed epic transition, never an approval.
func autoParentCloseUndoScope(a *ActionJournalRecord) (TransitionRecord, bool, error) {
	fail := func() (TransitionRecord, bool, error) { return TransitionRecord{}, false, nil }
	if a.Action != "update" || a.Before == nil || a.After == nil || a.Before.State != "open" || a.After.State != "closed" || a.entityKind() != "" {
		return fail()
	}
	b, err := actionStateRecord(a.Before, a.IssueID)
	if err != nil {
		return TransitionRecord{}, false, err
	}
	after, err := actionStateRecord(a.After, a.IssueID)
	if err != nil {
		return TransitionRecord{}, false, err
	}
	if b.Type != models.TypeEpic || b.DeletedAt != nil || after.DeletedAt != nil || after.Status != models.StatusClosed {
		return fail()
	}
	bd, err := b.CopyDetails()
	if err != nil {
		return TransitionRecord{}, false, err
	}
	ad, err := after.CopyDetails()
	if err != nil {
		return TransitionRecord{}, false, err
	}
	if len(ad.Transitions) != len(bd.Transitions)+1 || !slices.EqualFunc(ad.Transitions[:len(bd.Transitions)], bd.Transitions, func(x, y TransitionRecord) bool { return reflect.DeepEqual(x, y) }) {
		return fail()
	}
	tr := ad.Transitions[len(bd.Transitions)]
	reason := "Auto-cascaded to closed (all children complete)"
	if tr.Action != "close" || tr.SessionID != a.SessionID || tr.OperationID == "" || tr.At.IsZero() || tr.From != b.Status || tr.To != models.StatusClosed || tr.Reason != reason || tr.Snapshot != nil || tr.RelatedIssueID != "" || tr.AdminReason != "" || tr.SelfCloseException != "" {
		return fail()
	}
	for _, old := range bd.Transitions {
		if old.OperationID == tr.OperationID {
			return fail()
		}
	}
	expected := bd
	supersedeReviews(&expected, tr.At)
	expected.Status = models.StatusClosed
	expected.ImplementerSession = b.ImplementerSession
	expected.ReviewHandoffID = ""
	expected.ReviewHandoffUpdatedAt = time.Time{}
	expected.ReviewBasis = ""
	expected.ReviewEvents = ""
	expected.ReviewRequestedBySession = ""
	expected.ClosedBySession = a.SessionID
	if len(ad.Sessions) != len(bd.Sessions)+1 || !slices.EqualFunc(ad.Sessions[:len(bd.Sessions)], bd.Sessions, func(x, y models.IssueSessionHistory) bool { return reflect.DeepEqual(x, y) }) {
		return fail()
	}
	row := ad.Sessions[len(bd.Sessions)]
	if row.ID == "" || row.IssueID != a.IssueID || row.SessionID != a.SessionID || row.Action != models.ActionSessionClosed || !row.CreatedAt.Equal(tr.At) {
		return fail()
	}
	for _, old := range bd.Sessions {
		if old.ID == row.ID {
			return fail()
		}
	}
	expected.Sessions = ad.Sessions
	expected.Transitions = ad.Transitions
	if !reflect.DeepEqual(expected, ad) {
		return fail()
	}
	bdesc, bm, _, err := decodeBody(a.Before.Body)
	if err != nil {
		return TransitionRecord{}, false, err
	}
	adesc, am, _, err := decodeBody(a.After.Body)
	if err != nil {
		return TransitionRecord{}, false, err
	}
	bm.Details = nil
	am.Details = nil
	bm.LastStateReason = reason
	labels := mirroredPriorityLabels(mirroredLabels(a.Before.Labels, models.StatusClosed), am.Priority)
	slices.Sort(labels)
	actual := slices.Clone(a.After.Labels)
	slices.Sort(actual)
	if bdesc != adesc || a.Before.Title != a.After.Title || !reflect.DeepEqual(bm, am) || !slices.Equal(labels, actual) {
		return fail()
	}
	return tr, true, nil
}

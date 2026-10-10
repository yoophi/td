package ghstore

import (
	"reflect"
	"slices"

	"github.com/marcus/td/internal/models"
)

// Logical deletion changes visibility and appends one actor transition. It
// preserves native state, claims and reviews; no other writer effect is allowed.
func deletionUndoScope(action *ActionJournalRecord) (bool, error) {
	if action.Action != "update" || action.Before == nil || action.After == nil || action.entityKind() != "" || action.Before.State != action.After.State {
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
	if bm.Details == nil || am.Details == nil {
		return false, nil
	}
	bd, ad := *bm.Details, *am.Details
	if (bd.DeletedAt == nil) == (ad.DeletedAt == nil) || len(ad.Transitions) != len(bd.Transitions)+1 || !slices.EqualFunc(bd.Transitions, ad.Transitions[:len(bd.Transitions)], func(a, b TransitionRecord) bool { return reflect.DeepEqual(a, b) }) {
		return false, nil
	}
	tr := ad.Transitions[len(bd.Transitions)]
	if tr.OperationID == "" || tr.SessionID != action.SessionID || tr.At.IsZero() || tr.From != bd.Status || tr.To != bd.Status || tr.RelatedIssueID != "" || tr.AdminReason != "" || tr.SelfCloseException != "" || tr.Snapshot != nil {
		return false, nil
	}
	for _, old := range bd.Transitions {
		if old.OperationID == tr.OperationID {
			return false, nil
		}
	}
	if bd.DeletedAt == nil {
		if tr.Action != "delete" || !ad.DeletedAt.Equal(tr.At) {
			return false, nil
		}
	} else if tr.Action != "restore" {
		return false, nil
	}
	expected := bd
	expected.DeletedAt = ad.DeletedAt
	expected.Transitions = ad.Transitions
	if !reflect.DeepEqual(expected, ad) {
		return false, nil
	}
	bm.Details, am.Details = nil, nil
	status := ad.Status
	if status == "" {
		status = models.Status(action.After.State)
	}
	labels := mirroredPriorityLabels(mirroredLabels(action.Before.Labels, status), am.Priority)
	afterLabels := slices.Clone(action.After.Labels)
	slices.Sort(labels)
	slices.Sort(afterLabels)
	return reflect.DeepEqual(bm, am) && bdesc == adesc && action.Before.Title == action.After.Title && slices.Equal(labels, afterLabels), nil
}

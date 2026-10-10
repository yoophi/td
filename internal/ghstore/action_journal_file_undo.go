package ghstore

import (
	"reflect"
	"slices"

	"github.com/marcus/td/internal/models"
)

// Recognize the complete single-target file writer effect, including review
// invalidation. Other workflow/graph changes cannot hitch a ride on this inverse.
func fileUndoScope(action *ActionJournalRecord) (bool, error) {
	if action.Action != "update" || action.Before == nil || action.After == nil || action.entityKind() != "" {
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
	bd, ad := IssueDetails{}, IssueDetails{}
	if bm.Details != nil {
		bd = *bm.Details
	}
	if am.Details != nil {
		ad = *am.Details
	}
	if bd.DeletedAt != nil || ad.DeletedAt != nil {
		return false, nil
	}
	if bd.Status == "" {
		bd.Status = models.Status(action.Before.State)
	}
	if ad.Status == "" {
		ad.Status = models.Status(action.After.State)
	}
	if reflect.DeepEqual(bd.Files, ad.Files) || len(bd.Files) == 0 && len(ad.Files) == 0 {
		return false, nil
	}
	if _, err := validatedLinkedFiles(action.IssueID, bd.Files); err != nil {
		return false, err
	}
	if _, err := validatedLinkedFiles(action.IssueID, ad.Files); err != nil {
		return false, err
	}
	bm.Details, am.Details = nil, nil
	bl := mirroredPriorityLabels(mirroredLabels(action.Before.Labels, ad.Status), am.Priority)
	al := slices.Clone(action.After.Labels)
	slices.Sort(bl)
	slices.Sort(al)
	if !reflect.DeepEqual(bm, am) || bdesc != adesc || action.Before.Title != action.After.Title || action.Before.State != action.After.State || !slices.Equal(bl, al) {
		return false, nil
	}
	expected, valid := revokedReviewDetails(bd, ad)
	if !valid {
		return false, nil
	}
	expected.Files = ad.Files
	return reflect.DeepEqual(expected, ad), nil
}

package ghstore

import (
	"reflect"
	"slices"

	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/workflow"
)

// Match the whole basic transition writer effect, rather than accepting an
// arbitrary status/claim/history edit. Native close/reopen and coordinated
// cascades need separate inverse policies; these transitions keep native open.
func workflowUndoScope(action *ActionJournalRecord) (bool, error) {
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
	if tr.SessionID != action.SessionID || tr.OperationID == "" || tr.At.IsZero() || tr.From != bd.Status || tr.To != ad.Status || tr.RelatedIssueID != "" || tr.AdminReason != "" || tr.SelfCloseException != "" {
		return false, nil
	}
	if tr.Snapshot != nil && (tr.Snapshot.IssueID != action.IssueID || tr.Snapshot.Event != tr.Action) {
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
	expected, valid := revokedReviewDetails(bd, ad)
	if !valid {
		return false, nil
	}
	for i, b := range bd.Reviews {
		if b.SupersededAt == nil && !ad.Reviews[i].SupersededAt.Equal(tr.At) {
			return false, nil
		}
	}
	expected.Status = ad.Status
	expected.ReviewRequestedBySession, expected.ClosedBySession = "", ""
	type participation struct {
		session string
		action  models.IssueSessionAction
	}
	delta := []participation{}
	add := func(session string, a models.IssueSessionAction) {
		if session != "" {
			delta = append(delta, participation{session, a})
		}
	}
	switch tr.Action {
	case "start":
		if ad.Status != models.StatusInProgress || bd.Status == models.StatusInProgress {
			return false, nil
		}
		if bd.ImplementerSession != "" && bd.ImplementerSession != action.SessionID {
			add(bd.ImplementerSession, models.ActionSessionUnstarted)
		}
		expected.ImplementerSession = action.SessionID
		add(action.SessionID, models.ActionSessionStarted)
	case "unstart":
		if ad.Status != models.StatusOpen || bd.Status != models.StatusInProgress && bd.Status != models.StatusOpen || bd.Status == models.StatusOpen && bd.ImplementerSession == "" {
			return false, nil
		}
		add(bd.ImplementerSession, models.ActionSessionUnstarted)
		expected.ImplementerSession = ""
	case "block":
		if ad.Status != models.StatusBlocked || bd.Status == models.StatusBlocked {
			return false, nil
		}
	case "unblock":
		if bd.Status != models.StatusBlocked || ad.Status != models.StatusOpen {
			return false, nil
		}
		add(bd.ImplementerSession, models.ActionSessionUnstarted)
		expected.ImplementerSession = ""
	default:
		return false, nil
	}
	if len(ad.Sessions) != len(bd.Sessions)+len(delta) || !slices.EqualFunc(ad.Sessions[:len(bd.Sessions)], bd.Sessions, func(a, b models.IssueSessionHistory) bool { return reflect.DeepEqual(a, b) }) {
		return false, nil
	}
	ids := map[string]bool{}
	for _, row := range bd.Sessions {
		ids[row.ID] = true
	}
	for i, want := range delta {
		row := ad.Sessions[len(bd.Sessions)+i]
		if row.ID == "" || ids[row.ID] || row.IssueID != action.IssueID || row.SessionID != want.session || row.Action != want.action || !row.CreatedAt.Equal(tr.At) {
			return false, nil
		}
		ids[row.ID] = true
	}
	expected.Sessions, expected.Transitions = ad.Sessions, ad.Transitions
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

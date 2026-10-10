package ghstore

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/marcus/td/internal/models"
)

// A relation inverse restores one source. Cascades remain distinct receipts;
// neither the scope recognizer nor executor silently changes another issue.
func relationUndoScope(action *ActionJournalRecord) (bool, error) {
	if action.Action != "update" || action.Before == nil || action.After == nil || action.entityKind() != "" || action.Before.State != action.After.State {
		return false, nil
	}
	_, bm, _, err := decodeBody(action.Before.Body)
	if err != nil {
		return false, err
	}
	_, am, _, err := decodeBody(action.After.Body)
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
	if bd.Status == "" {
		bd.Status = models.Status(action.Before.State)
	}
	if ad.Status == "" {
		ad.Status = models.Status(action.After.State)
	}
	if bd.DeletedAt != nil || ad.DeletedAt != nil || bd.Status != ad.Status || bm.EntityKind != am.EntityKind || bm.OperationID != am.OperationID || bm.LastStateReason != am.LastStateReason || bd.ParentID == ad.ParentID && slices.Equal(bd.Dependencies, ad.Dependencies) {
		return false, nil
	}
	if !relationUndoTransitions(action, bd, ad) {
		return false, nil
	}
	if _, err := validatedLinkedFiles(action.IssueID, bd.Files); err != nil {
		return false, err
	}
	if _, err := validatedLinkedFiles(action.IssueID, ad.Files); err != nil {
		return false, err
	}
	normalize := func(d IssueDetails) IssueDetails {
		d.ParentID, d.Dependencies, d.Transitions = "", nil, nil
		d.Files = nil
		d.Minor, d.Sprint, d.DueDate, d.DeferUntil, d.DeferCount = false, "", nil, nil, 0
		return d
	}
	if reflect.DeepEqual(normalize(bd), normalize(ad)) {
		return true, nil
	}
	expected, valid := revokedReviewDetails(bd, ad)
	return valid && reflect.DeepEqual(normalize(expected), normalize(ad)), nil
}

// Dependency writers may append an exact delta or preserve legacy history.
// An unrelated transition, rewritten prefix, incomplete delta or another actor
// cannot be bundled into a graph restoration.
func relationUndoTransitions(action *ActionJournalRecord, bd, ad IssueDetails) bool {
	if len(ad.Transitions) < len(bd.Transitions) || !slices.EqualFunc(bd.Transitions, ad.Transitions[:len(bd.Transitions)], func(a, b TransitionRecord) bool { return reflect.DeepEqual(a, b) }) {
		return false
	}
	delta := ad.Transitions[len(bd.Transitions):]
	if len(delta) == 0 {
		return true
	}
	want := map[string]bool{}
	for _, id := range bd.Dependencies {
		if !slices.Contains(ad.Dependencies, id) {
			want["remove_dep:"+id] = true
		}
	}
	for _, id := range ad.Dependencies {
		if !slices.Contains(bd.Dependencies, id) {
			want["add_dep:"+id] = true
		}
	}
	if len(delta) != len(want) {
		return false
	}
	op := ""
	var at time.Time
	for _, tr := range delta {
		key := tr.Action + ":" + tr.RelatedIssueID
		if !want[key] || tr.SessionID != action.SessionID || tr.From != bd.Status || tr.To != ad.Status || tr.Snapshot != nil || tr.Reason != "" {
			return false
		}
		delete(want, key)
		if op == "" {
			op, at = tr.OperationID, tr.At
		}
		if op != tr.OperationID || !at.Equal(tr.At) {
			return false
		}
	}
	return len(want) == 0
}

// Each fence uses one complete issue collection, independent of graph size.
// Freeze only the reachable dependency/ancestor revisions; unrelated edits do
// not invalidate undo. The source revision is also compared with its point read.
func (c *Client) observeRelationUndoGraph(ctx context.Context, action *ActionJournalRecord, current *Record, afterWrite bool) (map[string]Record, error) {
	supported, err := relationUndoScope(action)
	if err != nil || !supported {
		return nil, err
	}
	if afterWrite {
		ctx = withReadbackCost(ctx)
	}
	snapshot, err := c.ReadSnapshot(WithFreshSnapshot(ctx), false)
	if err != nil {
		return nil, fmt.Errorf("undo graph observation failed (source write attempted=%t): %w", afterWrite, err)
	}
	all := map[string]Record{}
	for _, record := range snapshot.Issues(true) {
		all[record.ID] = record
	}
	return relationUndoGraph(action, current, all, afterWrite)
}

func relationUndoGraph(action *ActionJournalRecord, current *Record, all map[string]Record, afterWrite bool) (map[string]Record, error) {
	fail := func(reason string) (map[string]Record, error) {
		return nil, fmt.Errorf("undo graph is invalid: %s: %w", reason, &ConflictError{ID: action.IssueID, AfterWrite: afterWrite})
	}
	source, exists := all[action.IssueID]
	if current == nil || current.ID != action.IssueID || !exists || source.DeletedAt != nil || source.revision != current.revision {
		return fail("source collection differs from its observed revision")
	}
	_, bm, _, err := decodeBody(action.Before.Body)
	if err != nil {
		return nil, err
	}
	d := IssueDetails{}
	if bm.Details != nil {
		d = *bm.Details
	}
	observed := map[string]Record{}
	visiting, visited := map[string]bool{}, map[string]bool{}
	var walk func(string) error
	walk = func(id string) error {
		if id == action.IssueID || visiting[id] {
			return fmt.Errorf("dependency cycle at %s", id)
		}
		if visited[id] {
			return nil
		}
		target, exists := all[id]
		if !exists || target.DeletedAt != nil {
			return fmt.Errorf("dependency %s is missing, deleted or is not a task", id)
		}
		visiting[id] = true
		observed[id] = target
		if target.Details != nil {
			for _, next := range target.Details.Dependencies {
				if err := walk(next); err != nil {
					return err
				}
			}
		}
		delete(visiting, id)
		visited[id] = true
		return nil
	}
	for _, id := range d.Dependencies {
		if err := walk(id); err != nil {
			return fail(err.Error())
		}
	}
	seen := map[string]bool{action.IssueID: true}
	for id := d.ParentID; id != ""; {
		if seen[id] {
			return fail("parent cycle at " + id)
		}
		seen[id] = true
		target, exists := all[id]
		if !exists || target.DeletedAt != nil {
			return fail("parent " + id + " is missing, deleted or is not a task")
		}
		observed[id] = target
		id = target.ParentID
	}
	return observed, nil
}

func (c *Client) recheckRelationUndoGraph(ctx context.Context, action *ActionJournalRecord, current *Record, expected map[string]Record, afterWrite bool) error {
	if expected == nil {
		return nil
	}
	actual, err := c.observeRelationUndoGraph(ctx, action, current, afterWrite)
	if err != nil {
		return err
	}
	if len(actual) != len(expected) {
		return &ConflictError{ID: action.IssueID, AfterWrite: afterWrite}
	}
	for id, previous := range expected {
		now, exists := actual[id]
		if !exists || now.revision != previous.revision {
			return &ConflictError{ID: action.IssueID, AfterWrite: afterWrite}
		}
	}
	return nil
}

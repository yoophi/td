package ghstore

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
)

// A completed command is one causal edge. Its individual effects cannot be
// used as independent edges: a field/comment command has intermediate clocks
// that do not represent its final restored state.
func completedGroupBridge(root *ActionJournalRecord, id string, history []ActionJournalRecord) (*ActionIssueState, []ActionJournalRecord, error) {
	fail := func() (*ActionIssueState, []ActionJournalRecord, error) {
		return nil, nil, &ConflictError{ID: id}
	}
	if root == nil || root.Group == nil || root.validate() != nil || root.Phase != "undone" || root.Group.Forward.Proof != "verified-group-receipts" || root.Group.Inverse.Proof != "verified-group-receipts" {
		return fail()
	}
	for _, observation := range []*ActionGroupObservation{root.Group.Forward, root.Group.Inverse} {
		if observation.SessionID != root.SessionID || !strings.EqualFold(observation.Actor, root.Author) {
			return fail()
		}
	}
	attempts := []*ActionJournalRecord{}
	witnesses := []ActionJournalRecord{*root}
	for i := range history {
		if history[i].GroupOperationID == root.OperationID {
			attempts = append(attempts, &history[i])
			witnesses = append(witnesses, history[i])
		}
	}
	chains, err := orderedGroupEffects(root, attempts)
	if err != nil {
		return nil, nil, err
	}
	if root.Group.pairedGroup() {
		slices.SortFunc(attempts, func(a, b *ActionJournalRecord) int { return a.GroupSequence - b.GroupSequence })
		if err := validatePairedGroupForward(root, attempts); err != nil {
			return nil, nil, err
		}
	}
	var restored *ActionIssueState
	for i, member := range root.Group.Members {
		forward, inverse := root.Group.Forward.Members[i], root.Group.Inverse.Members[i]
		chain := chains[member.IssueID]
		if inverse.Status != "undone" {
			// No-op/unattempted effects cannot establish a new clock.
			if inverse.State.Revision != member.Before.Revision || forward.State.Revision != member.Before.Revision {
				return fail()
			}
			continue
		}
		if len(chain) == 0 || chain[len(chain)-1].After == nil || forward.Status != "applied" || forward.ReceiptID != chain[len(chain)-1].ID || inverse.ReceiptID != forward.ReceiptID || forward.State.Revision != chain[len(chain)-1].After.Revision {
			return fail()
		}
		source := chain[len(chain)-1].After
		for j := len(chain) - 1; j >= 0; j-- {
			effect := chain[j]
			if effect.Phase != "undone" || effect.Restored == nil || effect.UpdateObservation != nil || effect.CommentObservation != nil || effect.UndoObservation != nil || effect.NativeUndo != nil || !sameActionContents(*effect.Before, *effect.Restored) || effect.Restored.UpdatedAt.Before(source.UpdatedAt) {
				return fail()
			}
			source = effect.Restored
		}
		if !reflect.DeepEqual(*source, inverse.State) || !sameActionContents(member.Before, *source) {
			return fail()
		}
		if member.IssueID == id {
			state := inverse.State
			restored = &state
		}
	}
	if restored == nil {
		return nil, nil, fmt.Errorf("completed group has no restored effect for %s", id)
	}
	return restored, witnesses, nil
}

func revisionEdgeCount(history []ActionJournalRecord, id, before string) int {
	count := 0
	for _, a := range history {
		if a.Group != nil {
			for _, member := range a.Group.Members {
				if member.IssueID == id && member.Before.Revision == before {
					count++
				}
			}
		} else if a.GroupOperationID == "" && a.IssueID == id && a.Before != nil && a.Before.Revision == before {
			count++
		}
	}
	return count
}

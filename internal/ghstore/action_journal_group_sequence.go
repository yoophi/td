package ghstore

import (
	"fmt"
	"slices"
)

// orderedGroupEffects validates original forward attempts, never compensation
// bridges. A repeated target starts at the preceding actual forward After;
// neither planned contents nor an inverse observation can establish that edge.
// Callers must separately fence current revisions and actual native events.
func orderedGroupEffects(group *ActionJournalRecord, attempts []*ActionJournalRecord) (map[string][]*ActionJournalRecord, error) {
	if group == nil || group.Group == nil {
		return nil, fmt.Errorf("ordered effects require an original group")
	}
	expected := map[string]ActionIssueState{}
	for _, member := range group.Group.Members {
		if _, found := expected[member.IssueID]; found {
			return nil, fmt.Errorf("ordered effects require unique initial membership")
		}
		expected[member.IssueID] = member.Before
	}
	ordered := slices.Clone(attempts)
	for _, a := range ordered {
		if a == nil {
			return nil, fmt.Errorf("ordered effects contain a missing receipt")
		}
		if _, err := CommentNumber(a.ID); err != nil {
			return nil, err
		}
	}
	slices.SortFunc(ordered, func(a, b *ActionJournalRecord) int {
		an, _ := CommentNumber(a.ID)
		bn, _ := CommentNumber(b.ID)
		if an < bn {
			return -1
		}
		if an > bn {
			return 1
		}
		return 0
	})
	chains := map[string][]*ActionJournalRecord{}
	operations := map[string]bool{}
	for i, a := range ordered {
		if operations[a.OperationID] || i > 0 && ordered[i-1].ID == a.ID {
			return nil, fmt.Errorf("ordered effects repeat a receipt or operation identity")
		}
		operations[a.OperationID] = true
		if i > 0 && a.CreatedAt.Before(ordered[i-1].CreatedAt) {
			return nil, fmt.Errorf("ordered effect creation clocks regress")
		}
		before, found := expected[a.IssueID]
		if !found {
			return nil, fmt.Errorf("ordered effect target is outside immutable membership")
		}
		if err := before.validate(); err != nil {
			return nil, err
		}
		if err := validateGroupMemberAtState(group, a, &before); err != nil {
			return nil, err
		}
		chain := chains[a.IssueID]
		if len(chain) > 0 {
			previous := chain[len(chain)-1]
			if previous.UpdateIntent.NativeEvents != nil {
				if a.UpdateIntent.NativeEvents == nil {
					return nil, fmt.Errorf("ordered effect omitted its predecessor's native event continuation")
				}
				if err := updateEventProof(previous.ActionJournal, *a.UpdateIntent.NativeEvents); err != nil {
					return nil, err
				}
			}
		}
		switch a.Phase {
		case "prepared":
			if a.After != nil || i != len(ordered)-1 {
				return nil, fmt.Errorf("an uncertain effect cannot authorize a later attempt")
			}
		case "applied", "undo_prepared", "undone":
			// Original After remains immutable even after an inverse. Restored
			// is deliberately never used to connect original forward effects.
			if a.After == nil {
				return nil, fmt.Errorf("ordered effect has no actual forward result")
			}
			if err := a.After.validate(); err != nil {
				return nil, err
			}
			want, err := a.UpdateIntent.Target.contents()
			if err != nil {
				return nil, err
			}
			if !sameActionContents(want, *a.After) || a.After.UpdatedAt.Before(before.UpdatedAt) {
				return nil, fmt.Errorf("ordered effect result differs from its original input")
			}
			expected[a.IssueID] = *a.After
		default:
			return nil, fmt.Errorf("ordered effect has an invalid phase")
		}
		chains[a.IssueID] = append(chains[a.IssueID], a)
	}
	return chains, nil
}

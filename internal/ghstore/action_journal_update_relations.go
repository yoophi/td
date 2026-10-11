package ghstore

import (
	"encoding/hex"
	"fmt"
	"reflect"
	"slices"
)

const actionUpdateRelationsJournalStart = actionJournalPrefix + "v27\n"
const actionUpdateRelationsMemberStart = actionJournalPrefix + "v28\n"
const updateRelationsContract = "update-relations"

// Each task receives at most one combined field/relationship update and one
// optional inline comment. Order fixes a safe graph-write order. Related input
// observations start at the original states; member receipts may bind only an
// actual earlier result of this same command, never a projected revision.
type ActionRelationUpdates struct {
	Selected       []string                   `json:"selected"`
	Order          []int                      `json:"order"`
	Inputs         []ActionUpdateActivity     `json:"inputs"`
	Watches        []ActionRelatedObservation `json:"watches,omitempty"`
	Blocked        map[string][]string        `json:"blocked,omitempty"`
	ForwardDigests []string                   `json:"forward_digests,omitempty"`
}

func (g *ActionGroup) updateRelationsGroup() bool { return g != nil && g.RelationUpdates != nil }
func (g *ActionGroup) fieldCommentGroup() bool {
	return g.updateCommentsGroup() || g.updateRelationsGroup()
}
func (g *ActionGroup) fieldCommentInputs() []ActionUpdateActivity {
	if g.updateRelationsGroup() {
		return g.RelationUpdates.Inputs
	}
	return g.UpdateComments.Inputs
}

func (a ActionJournal) validateUpdateRelationsGroup() error {
	g := a.Group
	if !g.updateRelationsGroup() {
		return nil
	}
	p := g.RelationUpdates
	if g.Command != updateRelationsContract || g.HandoffReview != nil || g.UpdateComments != nil || g.UpdateActivity != nil || g.Ordered != nil || g.Planned || g.Cascade || len(g.Roots) != 0 || len(p.Inputs) != len(g.Members) || len(p.Order) != len(g.Members) || len(p.Selected) == 0 || p.Selected[0] != a.IssueID {
		return fmt.Errorf("relationship command requires complete selected tasks, inputs and effect order")
	}
	states := map[string]ActionIssueState{}
	for _, member := range g.Members {
		if member.Input != nil || member.CommentInput != nil || member.Noop {
			return fmt.Errorf("relationship command membership cannot replace its declared inputs")
		}
		states[member.IssueID] = member.Before
	}
	selected := map[string]bool{}
	for _, id := range p.Selected {
		if _, found := states[id]; !found || selected[id] {
			return fmt.Errorf("relationship command has missing or repeated selected tasks")
		}
		selected[id] = true
	}
	order := map[int]bool{}
	for _, index := range p.Order {
		if index < 0 || index >= len(g.Members) || order[index] {
			return fmt.Errorf("relationship command has invalid or repeated effect order")
		}
		order[index] = true
	}
	for _, watch := range p.Watches {
		if _, exists := states[watch.ID]; exists || !canonicalGroupID(watch.ID) || watch.ID == a.CarrierID {
			return fmt.Errorf("relationship command repeats or replaces a watched target")
		}
		if err := watch.State.validate(); err != nil {
			return err
		}
		r, err := actionStateRecord(&watch.State, watch.ID)
		if err != nil || r.DeletedAt != nil || r.meta.EntityKind != "" && r.meta.EntityKind != "issue" {
			return fmt.Errorf("relationship command watch is not a live task")
		}
		states[watch.ID] = watch.State
	}
	for source, ids := range p.Blocked {
		if !selected[source] || ids == nil || !slices.IsSorted(ids) || len(slices.Compact(slices.Clone(ids))) != len(ids) {
			return fmt.Errorf("relationship command has invalid reverse membership")
		}
		observed := []string{}
		for id, state := range states {
			r, err := actionStateRecord(&state, id)
			if err != nil {
				return err
			}
			if r.Details != nil && slices.Contains(r.Details.Dependencies, source) {
				observed = append(observed, id)
			}
		}
		slices.Sort(observed)
		if !slices.Equal(ids, observed) {
			return fmt.Errorf("relationship command omitted original reverse membership")
		}
	}
	operations := map[string]bool{}
	for i, input := range p.Inputs {
		m := g.Members[i]
		if len(input.ForwardDigests) != 0 || !selected[m.IssueID] && input.Comment != nil {
			return fmt.Errorf("relationship command has undeclared activity or nested proof")
		}
		requested := m.Before
		if input.Update != nil {
			probe := ActionJournal{OperationID: "probe-update", SessionID: a.SessionID, IssueID: m.IssueID, CarrierID: a.CarrierID, Action: "update", Phase: "prepared", Before: &m.Before, UpdateIntent: input.Update}
			if err := probe.validate(); err != nil {
				return err
			}
			var err error
			requested, err = input.Update.Target.contents()
			if err != nil {
				return err
			}
			requested.UpdatedAt = m.Before.UpdatedAt
			requested.Revision, err = requested.checksum()
			if err != nil {
				return err
			}
			probe.After = &requested
			if matched, err := relationUndoScope(&ActionJournalRecord{ActionJournal: probe}); err != nil {
				return err
			} else if !matched {
				if err := scalarFieldUndoScope(&ActionJournalRecord{ActionJournal: probe}); err != nil {
					return err
				}
			}
			for _, related := range input.Update.Related {
				if state, found := states[related.ID]; !found || !reflect.DeepEqual(state, related.State) {
					return fmt.Errorf("relationship input replaced an original graph observation")
				}
			}
		}
		if input.Comment != nil {
			probe := ActionJournal{OperationID: "probe-comment", SessionID: a.SessionID, IssueID: m.IssueID, CarrierID: a.CarrierID, Action: "comment_create", Phase: "prepared", Before: &requested, CommentInput: input.Comment}
			if err := probe.validate(); err != nil {
				return err
			}
			activity, err := decodeActivity(apiComment{ID: 1, Body: input.Comment.Body}, m.IssueID)
			if err != nil || activity.Kind != "comment" || activity.SessionID != a.SessionID || operations[input.Comment.OperationID] {
				return fmt.Errorf("relationship command requires distinct attributed inline comments")
			}
			operations[input.Comment.OperationID] = true
		}
	}
	if len(p.ForwardDigests) > g.pairedEffectCount() || a.Phase == "prepared" && len(p.ForwardDigests) != 0 {
		return fmt.Errorf("relationship command has invalid original effect membership")
	}
	for _, digest := range p.ForwardDigests {
		data, err := hex.DecodeString(digest)
		if err != nil || len(data) != 32 {
			return fmt.Errorf("invalid relationship command effect digest")
		}
	}
	return nil
}

func validateUpdateRelationsMember(root, member *ActionJournalRecord) error {
	g := root.Group
	if member.GroupContract != updateRelationsContract || member.GroupMixed || member.GroupSequence < 1 || member.GroupSequence > g.pairedEffectCount() {
		return fmt.Errorf("relationship member has an undeclared effect")
	}
	effect := g.updateCommentEffects()[member.GroupSequence-1]
	input := g.RelationUpdates.Inputs[effect.member]
	if member.IssueID != g.Members[effect.member].IssueID || member.Action != effect.action {
		return fmt.Errorf("relationship member replaced its target or effect order")
	}
	if effect.action == "update" {
		if member.GroupPreviousReceiptID != "" || member.CommentInput != nil || member.UpdateIntent == nil || !reflect.DeepEqual(member.UpdateIntent.Target, input.Update.Target) || !reflect.DeepEqual(member.UpdateIntent.NativeEvents, input.Update.NativeEvents) || len(member.UpdateIntent.Related) != len(input.Update.Related) {
			return fmt.Errorf("relationship update differs from its complete input")
		}
		for i, related := range input.Update.Related {
			if member.UpdateIntent.Related[i].ID != related.ID {
				return fmt.Errorf("relationship member replaced graph observation membership")
			}
		}
	} else if (member.GroupPreviousReceiptID != "") != (input.Update != nil) || member.UpdateIntent != nil || !reflect.DeepEqual(member.CommentInput, input.Comment) {
		return fmt.Errorf("relationship comment differs from its complete input")
	}
	return member.validate()
}

func validateRelationUpdateBindings(root, member *ActionJournalRecord, expected map[string]ActionIssueState) error {
	if member.UpdateIntent == nil {
		return nil
	}
	for _, related := range member.UpdateIntent.Related {
		state, found := expected[related.ID]
		if !found {
			for _, watch := range root.Group.RelationUpdates.Watches {
				if watch.ID == related.ID {
					state, found = watch.State, true
				}
			}
		}
		if !found || !reflect.DeepEqual(state, related.State) {
			return fmt.Errorf("relationship observation is not an original target or actual command predecessor")
		}
	}
	return nil
}

func validateUpdateRelationsRewrite(old, next *ActionRelationUpdates, completed bool) error {
	if old == nil || next == nil {
		if old != nil || next != nil {
			return fmt.Errorf("relationship command identity is immutable")
		}
		return nil
	}
	a, b := *old, *next
	a.ForwardDigests, b.ForwardDigests = nil, nil
	if !reflect.DeepEqual(a, b) || completed && !slices.Equal(old.ForwardDigests, next.ForwardDigests) {
		return fmt.Errorf("relationship command inputs and original effects are immutable")
	}
	return nil
}

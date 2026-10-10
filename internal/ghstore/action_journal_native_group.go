package ghstore

import (
	"context"
	"crypto/rand"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"
)

func nativeGroupCommand(command string) bool {
	return slices.Contains([]string{"close", "approve", "reopen"}, command)
}

func (c *Client) BeginTransitionActionGroup(ctx context.Context, command string, ids []string, o TransitionOptions) error {
	if !nativeGroupCommand(command) {
		return c.BeginActionGroup(ctx, command, ids)
	}
	g := &ActionGroup{Command: command, Roots: slices.Clone(ids), Cascade: command == "close" || command == "approve" && !o.RecordOnly}
	return c.beginGraphActionGroup(ctx, g)
}

func graphGroupIDs(g *ActionGroup, records []Record) ([]string, error) {
	if g.Command == "review" {
		return reviewGroupIDs(records, g.Roots)
	}
	all := map[string]Record{}
	for _, r := range records {
		if r.DeletedAt == nil && (r.meta.EntityKind == "" || r.meta.EntityKind == "issue") {
			if _, dup := all[r.ID]; dup {
				return nil, fmt.Errorf("native group listing repeated %s", r.ID)
			}
			all[r.ID] = r
		}
	}
	ids := []string{}
	seen := map[string]bool{}
	roots := map[string]bool{}
	add := func(id string) {
		if _, exists := all[id]; exists && !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	for _, root := range g.Roots {
		if !canonicalGroupID(root) || roots[root] {
			return nil, fmt.Errorf("native group requires distinct canonical roots")
		}
		roots[root] = true
		r, ok := all[root]
		if !ok {
			return nil, fmt.Errorf("native root %s missing or deleted", root)
		}
		add(root)
		if !g.Cascade {
			continue
		}
		closures := []string{root}
		chain := map[string]bool{root: true}
		for r.ParentID != "" {
			id := r.ParentID
			if chain[id] {
				return nil, fmt.Errorf("native group parent cycle at %s", id)
			}
			chain[id] = true
			r, ok = all[id]
			if !ok {
				return nil, fmt.Errorf("native parent %s missing or deleted", id)
			}
			closures = append(closures, id)
			add(id)
		}
		siblings := []string{}
		for id, r := range all {
			if slices.Contains(closures[1:], r.ParentID) {
				siblings = append(siblings, id)
			}
		}
		slices.Sort(siblings)
		for _, id := range siblings {
			add(id)
		}
		for _, closed := range closures {
			dependents := []string{}
			for id, r := range all {
				if r.Details != nil && slices.Contains(r.Details.Dependencies, closed) {
					dependents = append(dependents, id)
				}
			}
			slices.Sort(dependents)
			for _, id := range dependents {
				add(id)
				deps := slices.Clone(all[id].Details.Dependencies)
				slices.Sort(deps)
				for _, dep := range deps {
					add(dep)
				}
			}
		}
	}
	return ids, nil
}

func groupClosureIDs(g *ActionGroup) (map[string]bool, error) {
	all := map[string]ActionGroupMember{}
	for _, m := range g.Members {
		all[m.IssueID] = m
	}
	ids := map[string]bool{}
	for _, root := range g.Roots {
		current := root
		seen := map[string]bool{}
		for current != "" {
			if seen[current] {
				return nil, fmt.Errorf("native group ancestor cycle")
			}
			seen[current] = true
			ids[current] = true
			m, ok := all[current]
			if !ok {
				return nil, fmt.Errorf("native group omitted ancestor")
			}
			r, err := actionStateRecord(&m.Before, current)
			if err != nil {
				return nil, err
			}
			current = r.ParentID
		}
	}
	return ids, nil
}

func nativeGroupMemberScope(g *ActionGroup, a *ActionJournalRecord) (bool, error) {
	tr, matched, err := nativeUndoScope(a)
	if err != nil {
		return false, err
	}
	if matched {
		if g.Command == "approve" && !g.Cascade {
			return false, nil
		}
		if slices.Contains(g.Roots, a.IssueID) && tr.Action == g.Command {
			return true, nil
		}
		if g.Cascade {
			_, aggregate, err := autoParentCloseUndoScope(a)
			if err != nil {
				return false, err
			}
			if aggregate {
				ids, err := groupClosureIDs(g)
				return err == nil && ids[a.IssueID], err
			}
		}
		return false, nil
	}
	if g.Command == "approve" && !g.Cascade && slices.Contains(g.Roots, a.IssueID) {
		return reviewTransitionUndoScope(a)
	}
	if !g.Cascade {
		return false, nil
	}
	matched, err = workflowUndoScope(a)
	if err != nil || !matched {
		return matched, err
	}
	_, meta, _, _ := decodeBody(a.After.Body)
	tr = meta.Details.Transitions[len(meta.Details.Transitions)-1]
	if tr.Action != "unblock" {
		return false, nil
	}
	ids, err := groupClosureIDs(g)
	if err != nil {
		return false, err
	}
	r, err := actionStateRecord(a.Before, a.IssueID)
	if err != nil {
		return false, err
	}
	for id := range ids {
		if r.Details != nil && slices.Contains(r.Details.Dependencies, id) && tr.Reason == "Auto-unblocked (dependency "+id+" closed)" {
			return true, nil
		}
	}
	return false, fmt.Errorf("native group member is not a declared dependent unblock")
}

func (c *Client) preflightNativeGroupUndo(ctx context.Context, a *ActionJournalRecord, current *Record, session, actor string) error {
	plan, err := a.PlanNativeIssueUndo(current, session, actor, "td-op-"+rand.Text(), time.Now().UTC())
	if err != nil {
		return err
	}
	events, err := c.readStateEvents(withReviewCost(ctx), a.IssueID)
	if err != nil {
		return err
	}
	n := &ActionNativeUndo{Plan: *plan, Baseline: events}
	_, err = c.checkNativeUndoFence(ctx, a, n, current, false)
	return err
}

func groupMemberInverseObserved(ctx context.Context, c *Client, a *ActionJournalRecord, current *Record, original ActionIssueState, pending bool) error {
	state, err := actionIssueState(current)
	if err != nil {
		return err
	}
	if a.NativeUndo != nil {
		expected, err := nativePlanContents(a.NativeUndo.Plan)
		if err != nil {
			return err
		}
		if !sameActionContents(expected, state) || state.UpdatedAt.Before(a.After.UpdatedAt) {
			return fmt.Errorf("native group compensation is unverified; no target replay")
		}
		_, err = c.checkNativeUndoFence(ctx, a, a.NativeUndo, current, true)
		return err
	}
	if pending {
		if !sameActionContents(original, state) || state.UpdatedAt.Before(a.After.UpdatedAt) {
			return fmt.Errorf("group inverse is unverified; no target replay")
		}
		return nil
	}
	return c.checkUndoOriginalReview(ctx, a, false)
}

func nativeGroupMemberProofObserved(a *ActionJournalRecord) bool {
	return a.UndoObservation != nil || a.NativeUndo != nil && !strings.EqualFold(a.NativeUndo.Proof, "verified-native-inverse")
}

// Private sizing probes never become observed results or stored receipts.
// Reserve native compensation audit growth as well as scalar restoration.
func projectGroupInverseState(group ActionJournal, member ActionGroupMember, after ActionIssueState) (ActionIssueState, error) {
	if !nativeGroupCommand(group.Group.Command) || member.Before.State == after.State {
		return member.Before, nil
	}
	a := ActionJournalRecord{ActionJournal: ActionJournal{OperationID: "td-op-" + strings.Repeat("x", 32), SessionID: group.SessionID, IssueID: member.IssueID, CarrierID: group.CarrierID, Action: "update", Phase: "applied", Before: &member.Before, After: &after}, Author: strings.Repeat("x", 39)}
	current, err := actionStateRecord(&after, member.IssueID)
	if err != nil {
		return ActionIssueState{}, err
	}
	plan, err := a.PlanNativeIssueUndo(current, group.SessionID, a.Author, "td-op-"+strings.Repeat("y", 32), time.Now().UTC())
	if err != nil {
		return ActionIssueState{}, fmt.Errorf("native group inverse capacity preflight: %w", err)
	}
	state, err := nativePlanContents(*plan)
	if err != nil {
		return state, err
	}
	state.UpdatedAt = after.UpdatedAt
	state.Revision, err = state.checksum()
	return state, err
}

func (c *Client) checkGroupNativeEventReceipts(ctx context.Context, receipts map[string]*ActionJournalRecord) error {
	for _, a := range receipts {
		if a.Phase == "prepared" {
			continue
		}
		if a.NativeUndo != nil {
			events, err := c.readStateEvents(withReviewCost(ctx), a.IssueID)
			if err != nil {
				return err
			}
			if err := nativeUndoEventProof(a.NativeUndo, events, true); err != nil {
				return err
			}
			if !reflect.DeepEqual(a.NativeUndo.EventsAfter, events) {
				return fmt.Errorf("completed native group event proof changed")
			}
			continue
		}
		if a.Before.State != a.After.State && a.UpdateIntent != nil && a.UpdateIntent.NativeEvents != nil {
			events, err := c.readStateEvents(withReviewCost(ctx), a.IssueID)
			if err != nil {
				return err
			}
			if err := updateEventProof(a.ActionJournal, events); err != nil {
				return err
			}
		}
	}
	return nil
}

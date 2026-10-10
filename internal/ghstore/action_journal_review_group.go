package ghstore

import (
	"context"
	"crypto/rand"
	"fmt"
	"slices"
)

// Freeze every descendant, ancestor and sibling used by review completion.
// Ordering follows first planned encounter; execution receipts retain the
// actual write order when explicit roots overlap with prior cascades.
func reviewGroupIDs(records []Record, roots []string) ([]string, error) {
	all := map[string]Record{}
	live := []Record{}
	for _, r := range records {
		if r.DeletedAt == nil && (r.meta.EntityKind == "" || r.meta.EntityKind == "issue") {
			if _, duplicate := all[r.ID]; duplicate {
				return nil, fmt.Errorf("review group listing repeated %s", r.ID)
			}
			all[r.ID] = r
			live = append(live, r)
		}
	}
	ids := []string{}
	seen := map[string]bool{}
	add := func(id string) {
		if !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	rootSeen := map[string]bool{}
	for _, root := range roots {
		if !canonicalGroupID(root) || rootSeen[root] {
			return nil, fmt.Errorf("review group requires distinct canonical roots; no target write attempted")
		}
		rootSeen[root] = true
		r, ok := all[root]
		if !ok {
			return nil, fmt.Errorf("review root %s missing or deleted", root)
		}
		add(root)
		descendants, err := descendantsFromRecords(live, root)
		if err != nil {
			return nil, err
		}
		for _, d := range descendants {
			add(d.ID)
		}
		ancestors := map[string]bool{root: true}
		parents := []string{}
		for r.ParentID != "" {
			id := r.ParentID
			if ancestors[id] {
				return nil, fmt.Errorf("review group parent cycle at %s", id)
			}
			ancestors[id] = true
			r, ok = all[id]
			if !ok {
				return nil, fmt.Errorf("review group parent %s missing or deleted", id)
			}
			add(id)
			parents = append(parents, id)
		}
		siblings := []string{}
		for id, r := range all {
			if slices.Contains(parents, r.ParentID) {
				siblings = append(siblings, id)
			}
		}
		slices.Sort(siblings)
		for _, id := range siblings {
			add(id)
		}
	}
	return ids, nil
}

func (c *Client) beginReviewActionGroup(ctx context.Context, roots []string) error {
	return c.beginGraphActionGroup(ctx, &ActionGroup{Command: "review", Roots: slices.Clone(roots)})
}

func (c *Client) beginGraphActionGroup(ctx context.Context, g *ActionGroup) error {
	roots := g.Roots
	if c.journal == nil || c.journal.Group != nil || len(roots) == 0 {
		return fmt.Errorf("graph group requires tracked roots; no target write attempted")
	}
	snapshot, err := c.ReadSnapshot(WithFreshSnapshot(ctx), false)
	if err != nil {
		return err
	}
	records := snapshot.Issues(true)
	ids, err := graphGroupIDs(g, records)
	if err != nil {
		return err
	}
	if len(ids) < 2 {
		return nil
	}
	byID := map[string]Record{}
	for _, r := range records {
		byID[r.ID] = r
	}
	expected := map[string]ActionIssueState{}
	for _, id := range ids {
		r := byID[id]
		state, err := actionIssueState(&r)
		if err != nil {
			return err
		}
		expected[id] = state
		g.Members = append(g.Members, ActionGroupMember{IssueID: id, Before: state})
	}
	carrier := c.journal.Carrier.ID
	if carrier == "" {
		carrier = "gh-2147483647"
		if _, used := expected[carrier]; used {
			carrier = "gh-2147483646"
		}
	}
	a := ActionJournal{OperationID: "td-op-" + rand.Text(), SessionID: c.journal.SessionID, IssueID: ids[0], CarrierID: carrier, Action: "group", Phase: "prepared", Group: g}
	if err := checkGroupArchiveCapacity(a, expected); err != nil {
		return fmt.Errorf("graph group capacity preflight: %w", err)
	}
	if err := c.EnableActionJournal(ctx, c.journal.SessionID); err != nil {
		return err
	}
	a.CarrierID = c.journal.Carrier.ID
	if err := c.verifyGraphGroupExpected(ctx, g, expected); err != nil {
		return err
	}
	journal, err := c.writePreparedJournal(ctx, a)
	if err != nil {
		return err
	}
	c.journal.Group = &activeActionGroup{Journal: journal, Expected: expected, Attempted: map[string]bool{}}
	return nil
}

func verifyGraphGroupMembership(g *ActionGroup, records []Record) error {
	if len(g.Roots) == 0 {
		return nil
	}
	ids, err := graphGroupIDs(g, records)
	if err != nil {
		return err
	}
	if len(ids) != len(g.Members) {
		return fmt.Errorf("group hierarchy/dependency membership changed")
	}
	for i, id := range ids {
		if id != g.Members[i].IssueID {
			return fmt.Errorf("group hierarchy/dependency membership/order changed")
		}
	}
	return nil
}

func (c *Client) verifyGraphGroupExpected(ctx context.Context, g *ActionGroup, expected map[string]ActionIssueState) error {
	snapshot, err := c.ReadSnapshot(WithFreshSnapshot(ctx), false)
	if err != nil {
		return err
	}
	records := snapshot.Issues(true)
	if err := verifyGraphGroupMembership(g, records); err != nil {
		return err
	}
	actual := map[string]ActionIssueState{}
	for _, r := range records {
		if _, ok := expected[r.ID]; ok {
			state, err := actionIssueState(&r)
			if err != nil {
				return err
			}
			actual[r.ID] = state
		}
	}
	for id, state := range expected {
		if actual[id].Revision != state.Revision {
			return &ConflictError{ID: id}
		}
	}
	return nil
}

// An explicit root retains its own options and is processed by its command
// callback rather than consumed by an earlier descendant/ancestor cascade.
func (c *Client) deferredReviewGroupRoot(id, current string) bool {
	if c.journal == nil || c.journal.Group == nil || id == current {
		return false
	}
	active := c.journal.Group
	return active.Journal.Group.Command == "review" && slices.Contains(active.Journal.Group.Roots, id) && !active.Attempted[id]
}

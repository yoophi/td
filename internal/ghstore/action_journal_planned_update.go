package ghstore

import (
	"context"
	"crypto/rand"
	"fmt"
	"reflect"
)

// UpdateMany freezes the full input for independent scalar updates before the
// first write. On failure it returns only completed, verified updates; the
// printed group receipt can reconcile uncertain effects without replaying them.
// Relationships, transitions and activity comments need their own mixed plan.
func (c *Client) UpdateMany(ctx context.Context, ids []string, change Changes) ([]Record, error) {
	records, err := c.selectPlannedTasks(ctx, ids, false)
	if err != nil {
		return nil, err
	}
	return c.updateManyObserved(ctx, records, change)
}

func (c *Client) selectPlannedTasks(ctx context.Context, ids []string, includeDeleted bool) ([]Record, error) {
	canonical := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		n, err := Number(id)
		if err != nil {
			return nil, err
		}
		id = fmt.Sprintf("gh-%d", n)
		if seen[id] {
			return nil, fmt.Errorf("batch update repeats %s; no write attempted", id)
		}
		seen[id] = true
		canonical = append(canonical, id)
	}
	snapshot, err := c.ReadSnapshot(WithFreshSnapshot(ctx), false)
	if err != nil {
		return nil, err
	}
	byID := map[string]Record{}
	for _, r := range snapshot.Issues(true) {
		byID[r.ID] = r
	}
	selected := make([]Record, 0, len(ids))
	for _, id := range canonical {
		r, found := byID[id]
		if !found || r.DeletedAt != nil && !includeDeleted {
			return nil, fmt.Errorf("batch update member %s missing or deleted; no write attempted", id)
		}
		selected = append(selected, r)
	}
	return selected, nil
}

func (c *Client) updateManyObserved(ctx context.Context, records []Record, change Changes) ([]Record, error) {
	if len(records) < 2 || c.journal == nil || c.journal.Group != nil {
		return nil, fmt.Errorf("batch update requires tracked multiple tasks and no active group; no write attempted")
	}
	if change.Details != nil || change.Status != nil || change.ParentID != nil || change.Reason != nil || len(change.journalRelated) != 0 {
		return nil, fmt.Errorf("batch scalar update cannot include relationships or workflow changes; no write attempted")
	}
	plans := make([]*issueUpdatePlan, 0, len(records))
	for i := range records {
		plan, err := c.prepareIssueUpdate(ctx, &records[i], change)
		if err != nil {
			return nil, fmt.Errorf("plan update %s (no write attempted): %w", records[i].ID, err)
		}
		plans = append(plans, plan)
	}
	if err := c.beginPlannedUpdateGroup(ctx, plans); err != nil {
		return nil, err
	}
	groupID := c.journal.Group.Journal.ID
	completed := []Record{}
	for _, plan := range plans {
		updated, err := c.writeIssueUpdate(ctx, plan)
		if err != nil {
			return completed, fmt.Errorf("update group %s stopped at %s; earlier changes remain, inspect and reconcile the group before retrying: %w", groupID, plan.observed.ID, err)
		}
		completed = append(completed, *updated)
	}
	if _, err := c.CompleteActionGroup(ctx); err != nil {
		return completed, err
	}
	return completed, nil
}

func (c *Client) beginPlannedUpdateGroup(ctx context.Context, plans []*issueUpdatePlan) error {
	return c.beginPlannedTaskGroup(ctx, "update", plans, false)
}

func (c *Client) beginPlannedTaskGroup(ctx context.Context, command string, plans []*issueUpdatePlan, declared bool) error {
	g := &ActionGroup{Command: command, Planned: declared, Members: []ActionGroupMember{}}
	expected, sizing := map[string]ActionIssueState{}, map[string]ActionIssueState{}
	for _, plan := range plans {
		id := plan.observed.ID
		if _, duplicate := expected[id]; duplicate {
			return fmt.Errorf("batch update repeats %s; no write attempted", id)
		}
		before, err := actionIssueState(plan.observed)
		if err != nil {
			return err
		}
		input, err := c.plannedUpdateIntent(ctx, plan.observed, plan.payload, nil)
		if err != nil {
			return err
		}
		g.Members = append(g.Members, ActionGroupMember{IssueID: id, Before: before, Input: input, Noop: plan.noop})
		expected[id] = before
		// Only a private capacity probe uses the planned contents with an old
		// observed clock. No future result or receipt is persisted here.
		projected, err := input.Target.contents()
		if err != nil {
			return err
		}
		projected.UpdatedAt = before.UpdatedAt
		projected.Revision, err = projected.checksum()
		if err != nil {
			return err
		}
		sizing[id] = projected
	}
	carrier := c.journal.Carrier.ID
	if carrier == "" {
		for n := 2147483647; ; n-- {
			carrier = fmt.Sprintf("gh-%d", n)
			if _, exists := expected[carrier]; !exists {
				break
			}
		}
	}
	a := ActionJournal{OperationID: "td-op-" + rand.Text(), SessionID: c.journal.SessionID,
		IssueID: g.Members[0].IssueID, CarrierID: carrier, Action: "group", Phase: "prepared", Group: g}
	if err := checkGroupArchiveCapacity(a, sizing); err != nil {
		return fmt.Errorf("planned group capacity preflight (no write attempted): %w", err)
	}
	for _, m := range g.Members {
		if m.Noop {
			continue
		}
		probe := ActionJournal{OperationID: "td-op-" + rand.Text(), SessionID: a.SessionID, IssueID: m.IssueID,
			CarrierID: carrier, Action: "update", Phase: "prepared", Before: &m.Before, UpdateIntent: m.Input, GroupOperationID: a.OperationID}
		if err := checkUpdateArchiveCapacity(probe); err != nil {
			return fmt.Errorf("planned member capacity preflight (no write attempted): %w", err)
		}
	}
	if err := c.verifyGroupExpected(ctx, expected); err != nil {
		return err
	}
	if err := c.verifyPlannedUpdateEvents(ctx, g); err != nil {
		return err
	}
	if err := c.EnableActionJournal(ctx, c.journal.SessionID); err != nil {
		return err
	}
	a.CarrierID = c.journal.Carrier.ID
	if err := c.verifyGroupExpected(ctx, expected); err != nil {
		return err
	}
	journal, err := c.writePreparedJournal(ctx, a)
	if err != nil {
		return err
	}
	c.journal.Group = &activeActionGroup{Journal: journal, Expected: expected, Attempted: map[string]bool{}, Plans: map[string]*issueUpdatePlan{}}
	for _, plan := range plans {
		c.journal.Group.Plans[plan.observed.ID] = plan
	}
	return nil
}

func (c *Client) verifyPlannedUpdateEvents(ctx context.Context, g *ActionGroup) error {
	for _, m := range g.Members {
		if m.Input.NativeEvents == nil {
			continue
		}
		events, err := c.readStateEvents(withReviewCost(ctx), m.IssueID)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(events, *m.Input.NativeEvents) {
			return fmt.Errorf("planned update native events changed for %s; inspect before continuing", m.IssueID)
		}
	}
	return nil
}

// Recheck the immutable plan and every watched task after writing a member
// intent and before PATCH, and again against the actual result before receipt.
func (c *Client) verifyPlannedUpdateGroup(ctx context.Context, after *Record) error {
	if c.journal != nil && c.journal.Group != nil && c.journal.Group.Journal.Group.handoffReviewGroup() {
		return c.verifyHandoffReviewForward(ctx, after)
	}
	if c.journal != nil && c.journal.Group != nil && c.journal.Group.Journal.Group.updateActivityGroup() {
		return c.verifyUpdateActivityForward(ctx, after)
	}
	if c.journal == nil || c.journal.Group == nil || (!c.journal.Group.Journal.Group.hasPlannedInputs() && !c.journal.Group.Journal.Group.activityGroup()) {
		return nil
	}
	active := c.journal.Group
	if active.Journal.Group.activityGroup() {
		return c.verifyActivityGroupForward(ctx, after)
	}
	body, err := renderExistingActionJournal(active.Journal)
	if err != nil {
		return err
	}
	n, err := CommentNumber(active.Journal.ID)
	if err != nil {
		return err
	}
	root, err := c.journalComment(ctx, fmt.Sprintf("/issues/comments/%d", n), nil, body, active.Journal.CarrierID)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(root, active.Journal) {
		return fmt.Errorf("planned update group intent changed; inspect before continuing")
	}
	expected := map[string]ActionIssueState{}
	for id, state := range active.Expected {
		expected[id] = state
	}
	if after != nil {
		state, err := actionIssueState(after)
		if err != nil {
			return err
		}
		expected[after.ID] = state
	}
	if err := c.verifyGroupExpected(ctx, expected); err != nil {
		return err
	}
	if active.Journal.Group.hasPlannedInputs() {
		return c.verifyPlannedUpdateEvents(ctx, active.Journal.Group)
	}
	return nil
}

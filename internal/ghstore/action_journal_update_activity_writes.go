package ghstore

import (
	"context"
	"crypto/rand"
	"fmt"
	"reflect"

	"github.com/marcus/td/internal/models"
)

// UpdateWithActivityObserved fixes both complete inputs before the first write.
// It requires action tracking and never retries an uncertain field/comment write.
func (c *Client) UpdateWithActivityObserved(ctx context.Context, observed *Record, change Changes, activity models.Activity, command string) (*Record, error) {
	if c.journal == nil || c.journal.Group != nil || activity.SessionID != c.journal.SessionID {
		return nil, fmt.Errorf("update/activity command requires its own tracked session")
	}
	plan, err := c.prepareIssueUpdate(ctx, observed, change)
	if err != nil {
		return nil, err
	}
	if len(plan.parents) != 0 || len(change.journalRelated) != 0 {
		return nil, fmt.Errorf("update/activity command cannot replace relationship operations")
	}
	if activity.OperationID == "" {
		activity.OperationID = "td-op-" + rand.Text()
	}
	data, body, err := prepareActivityInput(observed.ID, activity)
	if err != nil {
		return nil, err
	}
	before, err := actionIssueState(observed)
	if err != nil {
		return nil, err
	}
	input, err := c.plannedUpdateIntent(ctx, observed, plan.payload, nil)
	if err != nil {
		return nil, err
	}
	carrier := "gh-2147483647"
	if carrier == observed.ID {
		carrier = "gh-2147483646"
	}
	g := &ActionGroup{Command: command, Members: []ActionGroupMember{{IssueID: observed.ID, Before: before}},
		UpdateActivity: &ActionUpdateActivity{Update: input, Comment: &ActionCommentInput{Body: body, OperationID: data.OperationID}}}
	root := ActionJournal{OperationID: "td-op-" + rand.Text(), SessionID: c.journal.SessionID, IssueID: observed.ID,
		CarrierID: carrier, Action: "group", Phase: "prepared", Group: g}
	projected, err := input.Target.contents()
	if err != nil {
		return nil, err
	}
	projected.UpdatedAt = before.UpdatedAt
	projected.Revision, err = projected.checksum()
	if err != nil {
		return nil, err
	}
	if err := checkGroupArchiveCapacity(root, map[string]ActionIssueState{observed.ID: projected}); err != nil {
		return nil, fmt.Errorf("update/activity capacity preflight (no write attempted): %w", err)
	}
	update := ActionJournal{OperationID: "td-op-" + rand.Text(), SessionID: root.SessionID, IssueID: root.IssueID, CarrierID: carrier,
		Action: "update", Phase: "prepared", Before: &before, UpdateIntent: input, GroupOperationID: root.OperationID, GroupSequence: 1, GroupMixed: true}
	if err := checkUpdateArchiveCapacity(update); err != nil {
		return nil, err
	}
	comment := ActionJournal{OperationID: "td-op-" + rand.Text(), SessionID: root.SessionID, IssueID: root.IssueID, CarrierID: carrier,
		Action: "comment_create", Phase: "prepared", Before: &projected, CommentInput: g.UpdateActivity.Comment,
		GroupOperationID: root.OperationID, GroupSequence: 2, GroupPreviousReceiptID: "ghc-9223372036854775807", GroupMixed: true}
	if err := checkActivityGroupMemberCapacity(comment); err != nil {
		return nil, err
	}
	if err := c.verifyGroupExpected(ctx, map[string]ActionIssueState{observed.ID: before}); err != nil {
		return nil, err
	}
	if err := c.verifyCommentOperation(ctx, observed.ID, data.OperationID, nil); err != nil {
		return nil, err
	}
	if err := c.EnableActionJournal(ctx, root.SessionID); err != nil {
		return nil, err
	}
	root.CarrierID = c.journal.Carrier.ID
	written, err := c.writePreparedJournal(ctx, root)
	if err != nil {
		return nil, err
	}
	c.journal.Group = &activeActionGroup{Journal: written, Expected: map[string]ActionIssueState{observed.ID: before}, Attempted: map[string]bool{}}
	fail := func(err error) (*Record, error) {
		return nil, fmt.Errorf("%s command group %s stopped; earlier effects may remain; inspect and reconcile this group before retrying: %w", command, written.ID, err)
	}
	updated, err := c.writeIssueUpdate(ctx, plan)
	if err != nil {
		return fail(err)
	}
	if _, err := c.appendJournaledActivity(ctx, updated, data, body); err != nil {
		return fail(err)
	}
	result, err := c.CompleteActionGroup(ctx)
	if err != nil {
		return fail(err)
	}
	return result.Issue, nil
}

func (c *Client) bindUpdateActivityMember(a *ActionJournal) error {
	active := c.journal.Group
	n := len(active.Receipts)
	if n > 1 || n == 1 && active.Receipts[0].Phase != "applied" {
		return fmt.Errorf("update/activity predecessor is not a completed original effect")
	}
	a.GroupMixed, a.GroupOperationID, a.GroupSequence = true, active.Journal.OperationID, n+1
	a.CarrierID = active.Journal.CarrierID
	if n == 1 {
		a.GroupPreviousReceiptID = active.Receipts[0].ID
	}
	before := active.Expected[a.IssueID]
	return validateGroupMemberAtState(active.Journal, &ActionJournalRecord{ActionJournal: *a, Author: active.Journal.Author}, &before)
}

func (c *Client) verifyUpdateActivityForward(ctx context.Context, after *Record) error {
	active := c.journal.Group
	state := active.Expected[active.Journal.IssueID]
	present := len(active.Receipts) == 2 && active.Receipts[1].Phase == "applied"
	if after != nil {
		var err error
		state, err = actionIssueState(after)
		if err != nil {
			return err
		}
		present = len(active.Receipts) == 2
	}
	return c.fenceUpdateActivity(ctx, active.Journal, active.Receipts, state, present)
}

func (c *Client) verifyUpdateActivityEvents(ctx context.Context, a *ActionJournalRecord) error {
	events := a.Group.UpdateActivity.Update.NativeEvents
	if events == nil {
		return nil
	}
	current, err := c.readStateEvents(ctx, a.IssueID)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(current, *events) {
		return fmt.Errorf("update/activity native state events changed")
	}
	return nil
}

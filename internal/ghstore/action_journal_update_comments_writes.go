package ghstore

import (
	"context"
	"crypto/rand"
	"fmt"

	"github.com/marcus/td/internal/models"
)

// UpdateWithComments groups scalar edits and inline comments, including a
// single selected task. Relationships and workflows use their own writers;
// callers must not route those changes through this independent-task contract.
func (c *Client) UpdateWithComments(ctx context.Context, ids []string, change Changes, activity models.Activity) ([]Record, error) {
	if c.journal == nil || c.journal.Group != nil || activity.SessionID != c.journal.SessionID || activity.Kind != "comment" || change.Status != nil || change.ParentID != nil || change.Details != nil || change.Reason != nil || len(change.journalRelated) != 0 {
		return nil, fmt.Errorf("update/comments requires tracked independent field edits and inline comments; no write attempted")
	}
	records, err := c.PreflightUpdate(ctx, ids, change, TransitionOptions{SessionID: activity.SessionID}, &activity)
	if err != nil {
		return nil, err
	}
	plans := make([]*issueUpdatePlan, len(records))
	activities := make([]models.Activity, len(records))
	g := &ActionGroup{Command: "update-comments", UpdateComments: &ActionUpdateComments{}}
	expected, sizing := map[string]ActionIssueState{}, map[string]ActionIssueState{}
	carrier := "gh-2147483647"
	for i := range records {
		r := &records[i]
		if r.ID == carrier {
			carrier = "gh-2147483646"
		}
		if change.HasFields() {
			plans[i], err = c.prepareIssueUpdate(ctx, r, change)
			if err != nil {
				return nil, err
			}
		}
		before, err := actionIssueState(r)
		if err != nil {
			return nil, err
		}
		var input *ActionUpdateIntent
		if plans[i] != nil {
			input, err = c.plannedUpdateIntent(ctx, r, plans[i].payload, nil)
			if err != nil {
				return nil, err
			}
		}
		activities[i] = activity
		activities[i].OperationID = "td-op-" + rand.Text()
		data, body, err := prepareActivityInput(r.ID, activities[i])
		if err != nil {
			return nil, err
		}
		g.Members = append(g.Members, ActionGroupMember{IssueID: r.ID, Before: before})
		g.UpdateComments.Inputs = append(g.UpdateComments.Inputs, ActionUpdateActivity{Update: input, Comment: &ActionCommentInput{Body: body, OperationID: data.OperationID}})
		expected[r.ID] = before
		projected := before
		if input != nil {
			projected, err = input.Target.contents()
			if err != nil {
				return nil, err
			}
		}
		projected.UpdatedAt = before.UpdatedAt
		projected.Revision, err = projected.checksum()
		if err != nil {
			return nil, err
		}
		sizing[r.ID] = projected
	}
	root := ActionJournal{OperationID: "td-op-" + rand.Text(), SessionID: c.journal.SessionID, IssueID: records[0].ID, CarrierID: carrier, Action: "group", Phase: "prepared", Group: g}
	if err := checkGroupArchiveCapacity(root, sizing); err != nil {
		return nil, fmt.Errorf("update/comments group capacity preflight (no write attempted): %w", err)
	}
	for sequence, item := range g.updateCommentEffects() {
		m, input := g.Members[item.member], g.UpdateComments.Inputs[item.member]
		effect := ActionJournal{OperationID: "td-op-" + rand.Text(), SessionID: root.SessionID, IssueID: m.IssueID, CarrierID: carrier, Action: item.action, Phase: "prepared", Before: &m.Before, GroupContract: updateCommentsContract, GroupOperationID: root.OperationID, GroupSequence: sequence + 1}
		if item.action == "update" {
			effect.UpdateIntent = input.Update
			if err := checkUpdateArchiveCapacity(effect); err != nil {
				return nil, err
			}
		} else {
			before := sizing[m.IssueID]
			effect.Before, effect.CommentInput = &before, input.Comment
			if input.Update != nil {
				effect.GroupPreviousReceiptID = "ghc-9223372036854775807"
			}
			if err := checkActivityGroupMemberCapacity(effect); err != nil {
				return nil, err
			}
		}
	}
	if err := c.verifyGroupExpected(ctx, expected); err != nil {
		return nil, err
	}
	if err := c.verifyPairedGroupEvents(ctx, &ActionJournalRecord{ActionJournal: root}); err != nil {
		return nil, err
	}
	if err := c.EnableActionJournal(ctx, root.SessionID); err != nil {
		return nil, err
	}
	root.CarrierID = c.journal.Carrier.ID
	if err := c.verifyGroupExpected(ctx, expected); err != nil {
		return nil, err
	}
	written, err := c.writePreparedJournal(ctx, root)
	if err != nil {
		return nil, err
	}
	c.journal.Group = &activeActionGroup{Journal: written, Expected: expected, Attempted: map[string]bool{}}
	completed := []Record{}
	for i, plan := range plans {
		r := &records[i]
		if plan != nil {
			r, err = c.writeIssueUpdate(ctx, plan)
			if err != nil {
				return completed, fmt.Errorf("update/comments group %s stopped at %s fields; earlier changes remain, inspect and reconcile the group before retrying: %w", written.ID, plan.observed.ID, err)
			}
		}
		if _, err := c.AppendActivity(ctx, r.ID, activities[i]); err != nil {
			if plan != nil {
				return completed, fmt.Errorf("issue %s was updated, but its comment failed in group %s; do not repeat the entire update, inspect and reconcile the group: %w", r.ID, written.ID, err)
			}
			return completed, fmt.Errorf("comment on %s failed in group %s; inspect and reconcile without replaying earlier comments: %w", r.ID, written.ID, err)
		}
		completed = append(completed, *r)
	}
	if _, err := c.CompleteActionGroup(ctx); err != nil {
		return completed, err
	}
	return completed, nil
}

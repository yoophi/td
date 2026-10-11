package ghstore

import (
	"context"
	"fmt"
	"reflect"

	"github.com/marcus/td/internal/models"
)

func (c *Client) observeWorkflowCompanion(snapshot *Snapshot, root, member *ActionJournalRecord, state ActionIssueState, out *orderedGroupObservation) (string, error) {
	deleting, deleted := false, false
	for _, step := range root.Group.Ordered.Steps {
		if step.ForwardReceiptID == member.ID && step.Comment != nil {
			deleting, deleted = true, step.Result != nil
		}
	}
	probe := *member
	if deleting {
		probe.CommentAfter = nil
	}
	comment, err := c.commentCreationInSnapshot(snapshot, &probe, state)
	if err != nil {
		return "", err
	}
	out.Comments[member.ID] = comment
	if deleting {
		if comment == nil {
			if err := deletedCommentInSnapshot(snapshot, member); err != nil {
				return "", err
			}
		} else if deleted || !reflect.DeepEqual(comment, member.CommentAfter) {
			return "", fmt.Errorf("workflow inverse companion changed or reappeared")
		}
	} else if member.Phase == "applied" && !reflect.DeepEqual(comment, member.CommentAfter) {
		return "", fmt.Errorf("workflow companion changed after its original result")
	}
	if member.Phase == "prepared" {
		if root.Phase != "prepared" {
			if comment != nil {
				return "", fmt.Errorf("unapplied workflow companion acquired a late result")
			}
			return "unapplied", nil
		}
		if comment == nil {
			if state.Revision != member.Before.Revision {
				return "", &ConflictError{ID: member.IssueID}
			}
			out.Partial = true
			return "unapplied", nil
		}
		if !sameActionContents(*member.Before, state) || state.UpdatedAt.Before(member.Before.UpdatedAt) {
			return "", &ConflictError{ID: member.IssueID}
		}
		out.PendingReceipt = member.ID
		return "pending-applied", nil
	}
	if member.Phase != "applied" {
		return "", fmt.Errorf("workflow companion acquired an individual inverse")
	}
	if effectWasObserved(member) {
		return "observed", nil
	}
	return "applied", nil
}

func workflowCommandComplete(root *ActionJournalRecord, effects []OrderedGroupEffect, states map[string]ActionIssueState) error {
	if !root.Group.workflowUpdateGroup() {
		return nil
	}
	w := root.Group.WorkflowUpdates
	seen := map[int]bool{}
	workflowSeen := map[int]bool{}
	for _, effect := range effects {
		if effect.Status == "applied" || effect.Status == "observed" {
			seen[effect.Record.WorkflowStep] = true
			step := w.Steps[effect.Record.WorkflowStep-1]
			if step.Kind == "workflow" && effect.Record.Action == "update" && effect.Record.IssueID == step.IssueID {
				workflowSeen[effect.Record.WorkflowStep] = true
			}
		}
	}
	for index, step := range w.Steps {
		if step.Update != nil || step.Comment != nil {
			if !seen[index+1] {
				return fmt.Errorf("workflow command has uncompleted requested fields or comments")
			}
		}
		if step.Kind != "workflow" {
			continue
		}
		state, exists := states[step.IssueID]
		if !exists {
			return fmt.Errorf("workflow command omitted its selected status result")
		}
		r, err := actionStateRecord(&state, step.IssueID)
		if err != nil {
			return err
		}
		want := w.Status
		if w.Options.RecordOnly && want == models.StatusClosed {
			want = models.StatusInReview
		}
		if r.Status != want || want == models.StatusInProgress && r.ImplementerSession != w.Options.SessionID {
			return fmt.Errorf("workflow command has an uncompleted requested status")
		}
		// Review submission always creates a new attributed request, even when
		// the displayed status was already in_review. A same-status field edit
		// alone cannot masquerade as completed review/record-only approval.
		if !workflowSeen[index+1] && (w.Status == models.StatusInReview || w.Options.RecordOnly) {
			return fmt.Errorf("workflow command omitted its requested review operation")
		}
	}
	return nil
}

func (c *Client) fenceWorkflowCompanions(ctx context.Context, observed *orderedGroupObservation) error {
	for _, comment := range observed.Comments {
		if comment != nil {
			if err := c.readNativeComment(ctx, comment); err != nil {
				return err
			}
		}
	}
	return nil
}

func preflightWorkflowInverseGraph(root *ActionJournalRecord, observed *orderedGroupObservation) error {
	if !root.Group.workflowUpdateGroup() {
		return nil
	}
	all := map[string]Record{}
	for id, r := range observed.Records {
		if r.DeletedAt == nil && (r.meta.EntityKind == "" || r.meta.EntityKind == "issue") {
			all[id] = r
		}
	}
	completed := 0
	for _, step := range root.Group.Ordered.Steps {
		if step.Result != nil {
			completed++
		}
	}
	effects := orderedAppliedEffects(root)
	for i := len(effects) - 1 - completed; i >= 0; i-- {
		effect := effects[i]
		if effect.Action != "update" {
			continue
		}
		if matched, err := relationUndoScope(effect); err != nil {
			return err
		} else if matched {
			current := all[effect.IssueID]
			if _, err := relationUndoGraph(effect, &current, all, false); err != nil {
				return err
			}
		}
		before, err := actionStateRecord(effect.Before, effect.IssueID)
		if err != nil {
			return err
		}
		all[effect.IssueID] = *before
	}
	return nil
}

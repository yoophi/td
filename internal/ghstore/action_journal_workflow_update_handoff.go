package ghstore

import (
	"context"
	"crypto/rand"
	"fmt"
	"reflect"

	"github.com/marcus/td/internal/models"
)

func workflowHandoffRequired(w *ActionWorkflowUpdates, original Record) bool {
	minor := original.Minor
	for _, step := range w.Steps {
		if step.Kind == "fields" && step.IssueID == original.ID && step.Update != nil {
			_, meta, _, err := decodeBody(step.Update.Target.Body)
			if err == nil && meta.Details != nil {
				minor = meta.Details.Minor
			}
		}
	}
	return w.Status == models.StatusInReview && !minor && !w.Options.Minor
}

func (c *Client) prepareWorkflowHandoffs(snapshot *Snapshot, w *ActionWorkflowUpdates, records map[string]Record) error {
	for _, id := range w.Selected {
		if !workflowHandoffRequired(w, records[id]) {
			continue
		}
		entries, err := snapshot.Activity(id)
		if err != nil {
			return err
		}
		var existing *models.Activity
		for i := len(entries) - 1; i >= 0; i-- {
			if entries[i].Kind == "handoff" {
				existing = &entries[i]
				break
			}
		}
		input := ActionWorkflowHandoff{}
		if existing != nil {
			number, err := CommentNumber(existing.ID)
			if err != nil {
				return err
			}
			for _, entry := range snapshot.entries {
				for _, comment := range entry.Comments {
					if comment.ID == number {
						input.Existing, err = c.nativeCommentState(comment, id, comment.Body)
						if err != nil {
							return err
						}
					}
				}
			}
			if input.Existing == nil {
				return fmt.Errorf("original workflow handoff is absent from native snapshot")
			}
		} else {
			done, decisions := []string{}, []string{}
			for _, activity := range entries {
				if activity.Kind == "log" {
					if activity.LogType == models.LogTypeDecision {
						decisions = append(decisions, activity.Message)
					} else {
						done = append(done, activity.Message)
					}
				}
			}
			if len(done) > 8 {
				done = done[len(done)-8:]
			}
			if len(done) == 0 {
				done = []string{"Auto-generated for review submission"}
			}
			data, body, err := prepareActivityInput(id, models.Activity{Kind: "handoff", SessionID: w.Options.SessionID, OperationID: "td-op-" + rand.Text(), Done: done, Decisions: decisions})
			if err != nil {
				return err
			}
			input.Create = &ActionCommentInput{Body: body, OperationID: data.OperationID}
		}
		if w.Handoffs == nil {
			w.Handoffs = map[string]ActionWorkflowHandoff{}
		}
		w.Handoffs[id] = input
	}
	return nil
}

func validateWorkflowHandoffInputs(a ActionJournal, plan *workflowUpdateGraph) error {
	w := a.Group.WorkflowUpdates
	records, err := workflowOriginalRecords(a.Group)
	if err != nil {
		return err
	}
	count := 0
	for _, id := range plan.Relations.Selected {
		required := workflowHandoffRequired(w, records[id])
		input, exists := w.Handoffs[id]
		if required != exists {
			return fmt.Errorf("workflow command omitted or added a review handoff input")
		}
		if !required {
			continue
		}
		count++
		if (input.Existing == nil) == (input.Create == nil) {
			return fmt.Errorf("workflow handoff must select exactly one existing result or creation input")
		}
		body := ""
		if input.Existing != nil {
			if err := input.Existing.validate(); err != nil {
				return err
			}
			if input.Existing.IssueID != id {
				return fmt.Errorf("workflow handoff belongs to another task")
			}
			body = input.Existing.Body
		} else {
			body = input.Create.Body
		}
		activity, err := decodeActivity(apiComment{ID: 1, Body: body}, id)
		if err != nil || activity.Kind != "handoff" {
			return fmt.Errorf("workflow handoff input is invalid")
		}
		if input.Create != nil && (activity.SessionID != a.SessionID || input.Create.OperationID == "" || activity.OperationID != input.Create.OperationID) {
			return fmt.Errorf("workflow automatic handoff has different attribution")
		}
	}
	if count != len(w.Handoffs) {
		return fmt.Errorf("workflow handoff is outside original selection")
	}
	return nil
}

func (c *Client) workflowReviewHandoff(ctx context.Context, observed *Record) (*Record, *models.Activity, error) {
	active := c.journal.Group
	input, found := active.Journal.Group.WorkflowUpdates.Handoffs[observed.ID]
	if !found {
		return nil, nil, fmt.Errorf("workflow review omitted its original handoff input")
	}
	if input.Existing != nil {
		if err := c.readNativeComment(ctx, input.Existing); err != nil {
			return nil, nil, err
		}
		n, _ := CommentNumber(input.Existing.ID)
		activity, err := decodeActivity(apiComment{ID: n, Body: input.Existing.Body, CreatedAt: input.Existing.CreatedAt, UpdatedAt: input.Existing.UpdatedAt}, observed.ID)
		return observed, &activity, err
	}
	activity, err := decodeActivity(apiComment{ID: 1, Body: input.Create.Body}, observed.ID)
	if err != nil {
		return nil, nil, err
	}
	data, body, err := prepareActivityInput(observed.ID, activity)
	if err != nil || body != input.Create.Body {
		return nil, nil, fmt.Errorf("workflow handoff input changed before publication")
	}
	result, err := c.appendJournaledActivityResult(ctx, observed, data, body)
	if err != nil {
		return nil, nil, err
	}
	return result.Issue, result.Activity, nil
}

func validateWorkflowReviewHandoffBinding(root, member *ActionJournalRecord, chain []*ActionJournalRecord) error {
	w := root.Group.WorkflowUpdates
	step := w.Steps[member.WorkflowStep-1]
	if step.Kind != "workflow" || member.IssueID != step.IssueID || member.UpdateIntent == nil {
		return nil
	}
	input, required := w.Handoffs[member.IssueID]
	if !required {
		return nil
	}
	_, meta, _, err := decodeBody(member.UpdateIntent.Target.Body)
	if err != nil || meta.Details == nil {
		return fmt.Errorf("workflow review omitted its actual handoff binding")
	}
	want := input.Existing
	if input.Create != nil {
		for _, previous := range chain {
			if previous.WorkflowStep == member.WorkflowStep && previous.CommentInput != nil && reflect.DeepEqual(previous.CommentInput, input.Create) && previous.Phase == "applied" {
				want = previous.CommentAfter
			}
		}
	}
	if want == nil || meta.Details.ReviewHandoffID != want.ID || !meta.Details.ReviewHandoffUpdatedAt.Equal(want.UpdatedAt) {
		return fmt.Errorf("workflow review used a projected or unrelated handoff result")
	}
	return nil
}

func (c *Client) fenceWorkflowHandoffs(ctx context.Context, root *ActionJournalRecord) error {
	if !root.Group.workflowUpdateGroup() {
		return nil
	}
	for _, input := range root.Group.WorkflowUpdates.Handoffs {
		if input.Existing != nil {
			if err := c.readNativeComment(ctx, input.Existing); err != nil {
				return err
			}
		}
	}
	return nil
}

// A later effect can refer to this command's own automatic handoff. Once its
// exact inverse deletion is observed, the frozen creation receipt and inverse
// checkpoint replace the live comment check. Existing handoffs and comments
// missing before a saved deletion input must still pass the original fence.
func workflowHandoffRemovalObserved(root, effect *ActionJournalRecord, details *IssueDetails, observed *orderedGroupObservation) bool {
	if !root.Group.workflowUpdateGroup() || observed == nil || (root.Phase != "undo_prepared" && root.Phase != "undone") {
		return false
	}
	input, found := root.Group.WorkflowUpdates.Handoffs[effect.IssueID]
	if !found || input.Create == nil || input.Existing != nil {
		return false
	}
	for _, forward := range root.Group.Ordered.Forward {
		created := forward.Record
		if forward.Status == "unapplied" || created.IssueID != effect.IssueID || created.GroupSequence >= effect.GroupSequence || !reflect.DeepEqual(created.CommentInput, input.Create) || created.CommentAfter == nil || created.CommentAfter.ID != details.ReviewHandoffID || !created.CommentAfter.UpdatedAt.Equal(details.ReviewHandoffUpdatedAt) {
			continue
		}
		comment, inspected := observed.Comments[created.ID]
		if !inspected || comment != nil {
			return false
		}
		for _, step := range root.Group.Ordered.Steps {
			if step.ForwardReceiptID == created.ID && reflect.DeepEqual(step.Comment, created.CommentAfter) {
				return step.Result != nil || observed.PendingStep && observed.PendingApplied
			}
		}
	}
	return false
}

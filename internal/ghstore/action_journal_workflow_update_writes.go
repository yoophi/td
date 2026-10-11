package ghstore

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"

	"github.com/marcus/td/internal/models"
)

// UpdateWorkflowCommand freezes the complete mixed command before any task
// write. Policy still runs after saved field edits, so an old approval can never
// approve newly edited contents. Partial failures retain one recoverable root.
func (c *Client) UpdateWorkflowCommand(ctx context.Context, ids []string, change Changes, relations RelationChanges, options TransitionOptions, activity *models.Activity) ([]Record, error) {
	return c.updateWorkflowCommand(ctx, ids, change, relations, options, activity, nil)
}

func (c *Client) updateWorkflowCommand(ctx context.Context, ids []string, change Changes, relations RelationChanges, options TransitionOptions, activity *models.Activity, observed *Record) ([]Record, error) {
	if c.journal == nil || c.journal.Group != nil || c.journal.SessionID != options.SessionID || change.Status == nil || activity != nil && (activity.Kind != "comment" || activity.SessionID != options.SessionID) {
		return nil, fmt.Errorf("workflow command requires a tracked actor, status and optional inline comment; no write attempted")
	}
	// Detach caller-owned pointers before asynchronous reads. Private observation
	// guards are checked separately; they are never encoded as requested inputs.
	type request struct {
		Change    Changes
		Relations RelationChanges
		Options   TransitionOptions
		Activity  *models.Activity
	}
	data, err := json.Marshal(request{change, relations, options, activity})
	if err != nil {
		return nil, err
	}
	var owned request
	if err := json.Unmarshal(data, &owned); err != nil {
		return nil, err
	}
	change, relations, options, activity = owned.Change, owned.Relations, owned.Options, owned.Activity
	if change.ParentID != nil && *change.ParentID != "" {
		n, err := Number(*change.ParentID)
		if err != nil {
			return nil, err
		}
		parent := fmt.Sprintf("gh-%d", n)
		change.ParentID = &parent
	}
	snapshot, err := c.ReadSnapshot(WithFreshSnapshot(ctx), *change.Status == models.StatusInReview)
	if err != nil {
		return nil, err
	}
	records := map[string]Record{}
	for _, r := range snapshot.Issues(false) {
		records[r.ID] = r
	}
	if observed != nil {
		if err := c.checkCommandObservation(records, observed); err != nil {
			return nil, err
		}
	}
	graph, err := planWorkflowUpdateGraph(records, ids, change, relations, options)
	if err != nil {
		return nil, err
	}
	w := &ActionWorkflowUpdates{Selected: graph.Relations.Selected, Status: *change.Status, ParentID: change.ParentID, DependsOn: relations.DependsOn, Blocks: relations.Blocks, Options: options, Baselines: map[string][]nativeStateEvent{}}
	g := &ActionGroup{Command: workflowUpdateContract, WorkflowUpdates: w, Ordered: &ActionOrderedGroup{}}
	expected := map[string]ActionIssueState{}
	for _, id := range graph.Members {
		r := records[id]
		before, err := actionIssueState(&r)
		if err != nil {
			return nil, err
		}
		g.Members = append(g.Members, ActionGroupMember{IssueID: id, Before: before})
		expected[id] = before
		w.Baselines[id], err = c.readStateEvents(ctx, id)
		if err != nil {
			return nil, err
		}
	}
	plans := map[int]*issueUpdatePlan{}
	for _, id := range graph.Relations.Order {
		r := records[id]
		fields := Changes{}
		if slices.Contains(w.Selected, id) {
			fields = change
			fields.Status = nil
		}
		plan, err := c.prepareRelationFields(ctx, &r, fields, graph.Relations.Desired[id])
		if err != nil {
			return nil, fmt.Errorf("workflow field preflight %s (no write attempted): %w", id, err)
		}
		step := ActionWorkflowStep{Kind: "fields", IssueID: id}
		if plan != nil && !plan.noop {
			for _, related := range graph.Relations.Related[id] {
				plan.change.journalRelated = append(plan.change.journalRelated, records[related])
			}
			related := append(slices.Clone(plan.parents), plan.change.journalRelated...)
			step.Update, err = c.plannedUpdateIntent(ctx, &r, plan.payload, related)
			if err != nil {
				return nil, err
			}
			events := slices.Clone(w.Baselines[id])
			if step.Update.NativeEvents != nil && !reflect.DeepEqual(*step.Update.NativeEvents, events) {
				return nil, &ConflictError{ID: id}
			}
			step.Update.NativeEvents = &events
			plans[len(w.Steps)+1] = plan
		}
		w.Steps = append(w.Steps, step)
	}
	for _, id := range w.Selected {
		w.Steps = append(w.Steps, ActionWorkflowStep{Kind: "workflow", IssueID: id})
	}
	if err := c.prepareWorkflowHandoffs(snapshot, w, records); err != nil {
		return nil, err
	}
	activities := map[int]activityData{}
	if activity != nil {
		for _, id := range w.Selected {
			input := *activity
			input.OperationID = "td-op-" + rand.Text()
			data, body, err := prepareActivityInput(id, input)
			if err != nil {
				return nil, err
			}
			activities[len(w.Steps)+1] = data
			w.Steps = append(w.Steps, ActionWorkflowStep{Kind: "comment", IssueID: id, Comment: &ActionCommentInput{Body: body, OperationID: data.OperationID}})
		}
	}
	if len(plans) == 0 && activity == nil {
		noop := true
		for _, id := range w.Selected {
			r := records[id]
			action := graph.Actions[id]
			if !groupBasicCommand(action) {
				noop = false
				break
			}
			_, unchanged, err := basicTransitionDecision(&r, action, options)
			if err != nil || !unchanged {
				noop = false
				break
			}
		}
		if noop {
			if err := c.verifyGraphGroupExpected(ctx, g, expected); err != nil {
				return nil, err
			}
			out := make([]Record, 0, len(w.Selected))
			for _, id := range w.Selected {
				out = append(out, records[id])
			}
			return out, nil
		}
	}
	carrier := "gh-2147483647"
	if _, exists := records[carrier]; exists {
		carrier = "gh-2147483646"
	}
	root := ActionJournal{OperationID: "td-op-" + rand.Text(), SessionID: options.SessionID, IssueID: w.Selected[0], CarrierID: carrier, Action: "group", Phase: "prepared", Group: g}
	if err := root.validate(); err != nil {
		return nil, err
	}
	if err := checkOrderedInitialCapacity(root, expected); err != nil {
		return nil, fmt.Errorf("workflow command capacity preflight (no write attempted): %w", err)
	}
	if err := c.EnableActionJournal(ctx, options.SessionID); err != nil {
		return nil, err
	}
	root.CarrierID = c.journal.Carrier.ID
	if err := c.verifyGraphGroupExpected(ctx, g, expected); err != nil {
		return nil, err
	}
	for id, baseline := range w.Baselines {
		events, err := c.readStateEvents(ctx, id)
		if err != nil {
			return nil, err
		}
		if !reflect.DeepEqual(baseline, events) {
			return nil, &ConflictError{ID: id}
		}
	}
	written, err := c.writePreparedJournal(ctx, root)
	if err != nil {
		return nil, err
	}
	active := &activeActionGroup{Journal: written, Expected: expected, Attempted: map[string]bool{}, NativeEvents: map[string][]nativeStateEvent{}}
	for id, baseline := range w.Baselines {
		active.NativeEvents[id] = slices.Clone(baseline)
	}
	c.journal.Group = active
	completed := []Record{}
	completedIDs := []string{}
	fail := func(err error) ([]Record, error) {
		return completed, fmt.Errorf("workflow group %s stopped; completed issues=%v; earlier field/relationship changes remain; inspect and reconcile without replay: %w", written.ID, completedIDs, err)
	}
	for index, step := range w.Steps {
		active.WorkflowStep = index + 1
		if step.Kind == "fields" && step.Update == nil {
			continue
		}
		current, err := c.Get(ctx, step.IssueID)
		if err != nil {
			return fail(err)
		}
		state, err := actionIssueState(current)
		if err != nil {
			return fail(err)
		}
		if state.Revision != active.Expected[step.IssueID].Revision {
			return fail(&ConflictError{ID: step.IssueID})
		}
		switch step.Kind {
		case "fields":
			plan := plans[index+1]
			if err := c.bindWorkflowFieldPlan(ctx, plan); err != nil {
				return fail(err)
			}
			if _, err := c.writeIssueUpdate(ctx, plan); err != nil {
				return fail(err)
			}
		case "workflow":
			action, err := workflowUpdateRequest(current.Status, w.Status, options)
			if err != nil {
				return fail(err)
			}
			result, _, err := c.TransitionObservedWithCascades(ctx, current, action, options)
			if err != nil {
				return fail(fmt.Errorf("%s field changes were saved, but requested %s transition failed: %w", current.ID, action, err))
			}
			completed, completedIDs = append(completed, *result), append(completedIDs, result.ID)
		case "comment":
			if _, err := c.appendJournaledActivityResult(ctx, current, activities[index+1], step.Comment.Body); err != nil {
				return fail(err)
			}
		}
	}
	if _, err := c.CompleteActionGroup(ctx); err != nil {
		return completed, err
	}
	return completed, nil
}

func (c *Client) bindWorkflowGroupMember(a *ActionJournal) error {
	active := c.journal.Group
	a.GroupContract, a.GroupOperationID, a.GroupSequence, a.WorkflowStep = workflowUpdateContract, active.Journal.OperationID, len(active.Receipts)+1, active.WorkflowStep
	for i := len(active.Receipts) - 1; i >= 0; i-- {
		if active.Receipts[i].IssueID == a.IssueID {
			a.GroupPreviousReceiptID = active.Receipts[i].ID
			break
		}
	}
	if a.CommentInput != nil {
		events := slices.Clone(active.NativeEvents[a.IssueID])
		a.WorkflowEvents = &events
	}
	return nil
}

func (c *Client) bindWorkflowFieldPlan(ctx context.Context, plan *issueUpdatePlan) error {
	for _, related := range [][]Record{plan.parents, plan.change.journalRelated} {
		for i, old := range related {
			r, err := c.Get(ctx, old.ID)
			if err != nil {
				return err
			}
			state, err := actionIssueState(r)
			if err != nil {
				return err
			}
			if state.Revision != c.journal.Group.Expected[old.ID].Revision {
				return &ConflictError{ID: old.ID}
			}
			related[i] = *r
		}
	}
	return nil
}

func verifyWorkflowGraphMembership(g *ActionGroup, actual []Record) error {
	records := map[string]Record{}
	for _, r := range actual {
		records[r.ID] = r
	}
	original, err := workflowOriginalRecords(g)
	if err != nil {
		return err
	}
	// Known members are revision-fenced separately. Overlay their original
	// graph to detect newly arriving external children/dependents at either
	// the original or requested hierarchy, even after own edits or inverses.
	for id, r := range original {
		records[id] = r
	}
	plan, err := g.WorkflowUpdates.graph(records)
	if err != nil {
		return err
	}
	ids := []string{}
	for _, m := range g.Members {
		ids = append(ids, m.IssueID)
	}
	if !slices.Equal(plan.Members, ids) {
		return fmt.Errorf("workflow hierarchy/dependency membership changed")
	}
	return nil
}

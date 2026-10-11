package ghstore

import (
	"context"
	"crypto/rand"
	"fmt"
	"slices"
	"time"

	"github.com/marcus/td/internal/models"
)

// UpdateWithRelations freezes one complete command, including reverse-edge
// targets, before writing any task. Repeated shared targets are written once.
func (c *Client) UpdateWithRelations(ctx context.Context, ids []string, change Changes, relations RelationChanges, activity *models.Activity) ([]Record, error) {
	return c.updateWithRelations(ctx, ids, change, relations, activity, nil)
}

func (c *Client) updateWithRelations(ctx context.Context, ids []string, change Changes, relations RelationChanges, activity *models.Activity, observed *Record) ([]Record, error) {
	if c.journal == nil || c.journal.Group != nil || change.Status != nil || change.Details != nil || change.Reason != nil || len(change.journalRelated) != 0 || activity != nil && (activity.Kind != "comment" || activity.SessionID != c.journal.SessionID) {
		return nil, fmt.Errorf("relationship update requires a tracked field/relationship command and optional inline comments; no write attempted")
	}
	snapshot, err := c.ReadSnapshot(WithFreshSnapshot(ctx), false)
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
	plan, err := c.prepareRelationCommand(ctx, records, ids, change, relations, activity)
	if err != nil {
		return nil, err
	}
	return c.writeRelationCommand(ctx, plan)
}

// A prepared command owns the original observations and every complete effect
// input. Separating preparation from execution lets creation bind an allocated
// ID without refreshing away the external observations captured before POST.
type relationCommandPlan struct {
	root       ActionJournal
	expected   map[string]ActionIssueState
	records    map[string]Record
	plans      []*issueUpdatePlan
	activities []models.Activity
}

func (c *Client) prepareRelationCommand(ctx context.Context, records map[string]Record, ids []string, change Changes, relations RelationChanges, activity *models.Activity) (*relationCommandPlan, error) {
	graph, err := planRelationUpdates(records, ids, relations)
	if err != nil {
		return nil, err
	}
	membership := slices.Clone(graph.Selected)
	for _, id := range graph.Order {
		if !slices.Contains(membership, id) {
			membership = append(membership, id)
		}
	}
	g := &ActionGroup{Command: updateRelationsContract, RelationUpdates: &ActionRelationUpdates{Selected: graph.Selected, Blocked: graph.Blocked}}
	expected, sizing := map[string]ActionIssueState{}, map[string]ActionIssueState{}
	plans := make([]*issueUpdatePlan, len(membership))
	activities := make([]models.Activity, len(membership))
	watches := map[string]ActionIssueState{}
	carrier := "gh-2147483647"
	for i, id := range membership {
		r := records[id]
		if id == carrier {
			carrier = "gh-2147483646"
		}
		fields := Changes{}
		if slices.Contains(graph.Selected, id) {
			fields = change
		}
		plan, err := c.prepareRelationFields(ctx, &r, fields, graph.Desired[id])
		if err != nil {
			return nil, fmt.Errorf("relationship update preflight %s (no write attempted): %w", id, err)
		}
		plans[i] = plan
		before, err := actionIssueState(&r)
		if err != nil {
			return nil, err
		}
		input := ActionUpdateActivity{}
		if plan != nil {
			for _, target := range graph.Related[id] {
				plan.change.journalRelated = append(plan.change.journalRelated, records[target])
			}
			related := append(slices.Clone(plan.parents), plan.change.journalRelated...)
			input.Update, err = c.plannedUpdateIntent(ctx, &r, plan.payload, related)
			if err != nil {
				return nil, err
			}
			for _, related := range input.Update.Related {
				watches[related.ID] = related.State
			}
		}
		if activity != nil && slices.Contains(graph.Selected, id) {
			activities[i] = *activity
			activities[i].OperationID = "td-op-" + rand.Text()
			data, body, err := prepareActivityInput(id, activities[i])
			if err != nil {
				return nil, err
			}
			input.Comment = &ActionCommentInput{Body: body, OperationID: data.OperationID}
		}
		g.Members = append(g.Members, ActionGroupMember{IssueID: id, Before: before})
		g.RelationUpdates.Inputs = append(g.RelationUpdates.Inputs, input)
		expected[id], sizing[id] = before, before
		if input.Update != nil {
			projected, err := input.Update.Target.contents()
			if err != nil {
				return nil, err
			}
			projected.UpdatedAt = before.UpdatedAt
			projected.Revision, err = projected.checksum()
			if err != nil {
				return nil, err
			}
			sizing[id] = projected
		}
	}
	for _, id := range graph.Order {
		g.RelationUpdates.Order = append(g.RelationUpdates.Order, slices.Index(membership, id))
	}
	for _, ids := range graph.Blocked {
		for _, id := range ids {
			r := records[id]
			state, err := actionIssueState(&r)
			if err != nil {
				return nil, err
			}
			watches[id] = state
		}
	}
	watchIDs := []string{}
	for id := range watches {
		if !slices.Contains(membership, id) {
			watchIDs = append(watchIDs, id)
		}
	}
	slices.Sort(watchIDs)
	for _, id := range watchIDs {
		g.RelationUpdates.Watches = append(g.RelationUpdates.Watches, ActionRelatedObservation{ID: id, State: watches[id]})
	}
	root := ActionJournal{OperationID: "td-op-" + rand.Text(), SessionID: c.journal.SessionID, IssueID: membership[0], CarrierID: carrier, Action: "group", Phase: "prepared", Group: g}
	if err := checkRelationCommandCapacity(root, sizing); err != nil {
		return nil, fmt.Errorf("relationship update capacity preflight (no write attempted): %w", err)
	}
	return &relationCommandPlan{root: root, expected: expected, records: records, plans: plans, activities: activities}, nil
}

func (c *Client) writeRelationCommand(ctx context.Context, command *relationCommandPlan) ([]Record, error) {
	root, expected, records := command.root, command.expected, command.records
	g, plans, activities := root.Group, command.plans, command.activities
	if err := c.verifyRelationGroupOriginal(ctx, g, expected); err != nil {
		return nil, err
	}
	if g.pairedEffectCount() == 0 {
		out := []Record{}
		for _, id := range g.RelationUpdates.Selected {
			out = append(out, records[id])
		}
		return out, nil
	}
	if err := c.verifyPairedGroupEvents(ctx, &ActionJournalRecord{ActionJournal: root}); err != nil {
		return nil, err
	}
	if err := c.EnableActionJournal(ctx, root.SessionID); err != nil {
		return nil, err
	}
	root.CarrierID = c.journal.Carrier.ID
	if err := c.verifyRelationGroupOriginal(ctx, g, expected); err != nil {
		return nil, err
	}
	written, err := c.writePreparedJournal(ctx, root)
	if err != nil {
		return nil, err
	}
	c.journal.Group = &activeActionGroup{Journal: written, Expected: expected, Attempted: map[string]bool{}}
	completed := []Record{}
	completedSources := []string{}
	for _, i := range g.RelationUpdates.Order {
		r := records[g.Members[i].IssueID]
		plan := plans[i]
		if plan != nil {
			if err := c.bindRelationPlan(ctx, plan); err != nil {
				return completed, fmt.Errorf("relationship group %s stopped before %s; completed sources=%v; earlier changes remain, inspect and reconcile before retrying: %w", written.ID, r.ID, completedSources, err)
			}
			updated, err := c.writeIssueUpdate(ctx, plan)
			if err != nil {
				return completed, fmt.Errorf("relationship group %s stopped at %s; completed sources=%v; earlier changes remain, inspect and reconcile without replay: %w", written.ID, r.ID, completedSources, err)
			}
			r = *updated
			completedSources = append(completedSources, r.ID)
		}
		if g.RelationUpdates.Inputs[i].Comment != nil {
			if _, err := c.AppendActivity(ctx, r.ID, activities[i]); err != nil {
				return completed, fmt.Errorf("comment on %s failed in relationship group %s; earlier changes remain, inspect and reconcile without replay: %w", r.ID, written.ID, err)
			}
		}
		if slices.Contains(g.RelationUpdates.Selected, r.ID) {
			completed = append(completed, r)
		}
	}
	if _, err := c.CompleteActionGroup(ctx); err != nil {
		return completed, err
	}
	// Output follows selection order even when safe graph order differs.
	slices.SortFunc(completed, func(a, b Record) int {
		return slices.Index(g.RelationUpdates.Selected, a.ID) - slices.Index(g.RelationUpdates.Selected, b.ID)
	})
	return completed, nil
}

func (c *Client) prepareRelationFields(ctx context.Context, source *Record, fields Changes, dependencies []string) (*issueUpdatePlan, error) {
	var plan *issueUpdatePlan
	var err error
	if fields.HasFields() {
		plan, err = c.prepareIssueUpdate(ctx, source, fields)
		if err != nil {
			return nil, err
		}
	}
	details, err := source.CopyDetails()
	if err != nil {
		return nil, err
	}
	if slices.Equal(details.Dependencies, dependencies) {
		return plan, nil
	}
	if plan != nil {
		before, err := actionIssueState(source)
		if err != nil {
			return nil, err
		}
		input, err := updateInputFromPayload(before, plan.payload)
		if err != nil {
			return nil, err
		}
		_, meta, _, err := decodeBody(input.Body)
		if err != nil {
			return nil, err
		}
		if meta.Details != nil {
			details = *meta.Details
		}
	}
	now, operation := time.Now().UTC(), "td-op-"+rand.Text()
	for _, old := range details.Dependencies {
		if !slices.Contains(dependencies, old) {
			details.Transitions = append(details.Transitions, TransitionRecord{OperationID: operation, Action: "remove_dep", From: source.Status, To: source.Status, SessionID: c.journal.SessionID, At: now, RelatedIssueID: old})
		}
	}
	for _, target := range dependencies {
		if !slices.Contains(details.Dependencies, target) {
			details.Transitions = append(details.Transitions, TransitionRecord{OperationID: operation, Action: "add_dep", From: source.Status, To: source.Status, SessionID: c.journal.SessionID, At: now, RelatedIssueID: target})
		}
	}
	details.Dependencies = slices.Clone(dependencies)
	supersedeReviews(&details, now)
	details.ReviewerSession, details.ReviewedAt, details.ReviewBasis = "", nil, ""
	fields.Details = &details
	fields.Minor, fields.Sprint, fields.ParentID, fields.DueDate, fields.DeferUntil = nil, nil, nil, nil, nil
	return c.prepareIssueUpdate(ctx, source, fields)
}

func checkRelationGroupCapacity(root ActionJournal, sizing map[string]ActionIssueState) error {
	if err := checkGroupArchiveCapacity(root, sizing); err != nil {
		return err
	}
	g := root.Group
	for sequence, effect := range g.updateCommentEffects() {
		member, input := g.Members[effect.member], g.RelationUpdates.Inputs[effect.member]
		a := ActionJournal{OperationID: "td-op-" + rand.Text(), SessionID: root.SessionID, IssueID: member.IssueID, CarrierID: root.CarrierID, Action: effect.action, Phase: "prepared", Before: &member.Before, GroupContract: updateRelationsContract, GroupOperationID: root.OperationID, GroupSequence: sequence + 1}
		if effect.action == "update" {
			a.UpdateIntent = input.Update
			if err := checkUpdateArchiveCapacity(a); err != nil {
				return err
			}
		} else {
			state := sizing[member.IssueID]
			a.Before, a.CommentInput = &state, input.Comment
			if input.Update != nil {
				a.GroupPreviousReceiptID = "ghc-9223372036854775807"
			}
			if err := checkActivityGroupMemberCapacity(a); err != nil {
				return err
			}
		}
	}
	return nil
}

// Rebind only actual related snapshots changed earlier by this very command.
// The target payload and source observation remain the original frozen input.
func (c *Client) bindRelationPlan(ctx context.Context, plan *issueUpdatePlan) error {
	active := c.journal.Group
	if err := c.verifyPairedGroupForward(ctx, nil); err != nil {
		return err
	}
	bind := func(records []Record) error {
		for i, before := range records {
			expected, own := active.Expected[before.ID]
			if !own {
				continue
			}
			current, err := c.Get(ctx, before.ID)
			if err != nil {
				return err
			}
			state, err := actionIssueState(current)
			if err != nil {
				return err
			}
			if state.Revision != expected.Revision {
				return &ConflictError{ID: before.ID}
			}
			records[i] = *current
		}
		return nil
	}
	if err := bind(plan.parents); err != nil {
		return err
	}
	return bind(plan.change.journalRelated)
}

func checkRelationCommandCapacity(root ActionJournal, sizing map[string]ActionIssueState) error {
	if root.Group.pairedEffectCount() == 0 {
		return nil
	}
	return checkRelationGroupCapacity(root, sizing)
}

package ghstore

import (
	"fmt"
	"reflect"
	"slices"

	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/reviewpolicy"
)

const actionWorkflowUpdateJournalStart = actionJournalPrefix + "v29\n"
const actionWorkflowUpdateMemberStart = actionJournalPrefix + "v30\n"
const workflowUpdateContract = "workflow-update"

// Steps declare requested effects, not their results. A workflow step may emit
// multiple actual updates (root, descendants, parents, dependent unblocks).
// Its options and potential membership are frozen before the first write.
type ActionWorkflowStep struct {
	Kind    string              `json:"kind"`
	IssueID string              `json:"issue_id"`
	Update  *ActionUpdateIntent `json:"update,omitempty"`
	Comment *ActionCommentInput `json:"comment,omitempty"`
}

type ActionWorkflowUpdates struct {
	Handoffs  map[string]ActionWorkflowHandoff `json:"handoffs,omitempty"`
	Selected  []string                         `json:"selected"`
	Status    models.Status                    `json:"status"`
	ParentID  *string                          `json:"parent_id,omitempty"`
	DependsOn *[]string                        `json:"depends_on,omitempty"`
	Blocks    *[]string                        `json:"blocks,omitempty"`
	Options   TransitionOptions                `json:"options"`
	Steps     []ActionWorkflowStep             `json:"steps"`
	Baselines map[string][]nativeStateEvent    `json:"baselines"`
}

type ActionWorkflowHandoff struct {
	Existing *ActionCommentState `json:"existing,omitempty"`
	Create   *ActionCommentInput `json:"create,omitempty"`
}

func (g *ActionGroup) workflowUpdateGroup() bool { return g != nil && g.WorkflowUpdates != nil }

func workflowOriginalRecords(g *ActionGroup) (map[string]Record, error) {
	records := map[string]Record{}
	for _, member := range g.Members {
		r, err := actionStateRecord(&member.Before, member.IssueID)
		if err != nil {
			return nil, err
		}
		records[member.IssueID] = *r
	}
	return records, nil
}

func (w *ActionWorkflowUpdates) graph(records map[string]Record) (*workflowUpdateGraph, error) {
	return planWorkflowUpdateGraph(records, w.Selected, Changes{Status: &w.Status, ParentID: w.ParentID}, RelationChanges{DependsOn: w.DependsOn, Blocks: w.Blocks}, w.Options)
}

func (a ActionJournal) validateWorkflowUpdateGroup() error {
	g := a.Group
	if !g.workflowUpdateGroup() {
		return nil
	}
	w := g.WorkflowUpdates
	if g.Command != workflowUpdateContract || g.Ordered == nil || g.Planned || g.Cascade || len(g.Roots) != 0 || g.RelationUpdates != nil || g.UpdateComments != nil || g.UpdateActivity != nil || g.HandoffReview != nil || w.Options.SessionID != a.SessionID || w.Options.expectedRevision != nil || w.Options.skipReviewHandoff || len(w.Selected) == 0 || w.Selected[0] != a.IssueID {
		return fmt.Errorf("workflow update requires one ordered command with its original actor and complete inputs")
	}
	records, err := workflowOriginalRecords(g)
	if err != nil {
		return err
	}
	plan, err := w.graph(records)
	if err != nil {
		return err
	}
	ids := make([]string, len(g.Members))
	for i, m := range g.Members {
		ids[i] = m.IssueID
		if m.Noop || m.Input != nil || m.CommentInput != nil {
			return fmt.Errorf("workflow membership cannot replace requested steps with projected outcomes")
		}
	}
	if !slices.Equal(ids, plan.Members) || !slices.Equal(w.Selected, plan.Relations.Selected) || len(w.Baselines) != len(g.Members) {
		return fmt.Errorf("workflow command omitted original graph membership or native event baselines")
	}
	for _, id := range ids {
		baseline, found := w.Baselines[id]
		if !found || baseline == nil {
			return fmt.Errorf("workflow command omitted an original native event baseline")
		}
		if err := orderedStepEventProof(&OrderedGroupUndoStep{Baseline: baseline}, baseline, false); err != nil {
			return err
		}
	}
	fields := len(plan.Relations.Order)
	minimum := fields + len(w.Selected)
	if len(w.Steps) != minimum && len(w.Steps) != minimum+len(w.Selected) {
		return fmt.Errorf("workflow command omitted or added requested effect slots")
	}
	operations := map[string]bool{}
	for i, step := range w.Steps {
		switch {
		case i < fields:
			if step.Kind != "fields" || step.IssueID != plan.Relations.Order[i] || step.Comment != nil {
				return fmt.Errorf("workflow field order differs from its complete relationship plan")
			}
			before := g.Members[slices.Index(ids, step.IssueID)].Before
			requested := before
			if step.Update != nil {
				requested, err = step.Update.Target.contents()
				if err != nil {
					return err
				}
				requested.UpdatedAt = before.UpdatedAt
				requested.Revision, err = requested.checksum()
				if err != nil {
					return err
				}
			}
			r, err := actionStateRecord(&requested, step.IssueID)
			if err != nil {
				return err
			}
			details, err := r.CopyDetails()
			if err != nil || !slices.Equal(details.Dependencies, plan.Relations.Desired[step.IssueID]) || w.ParentID != nil && slices.Contains(w.Selected, step.IssueID) && r.ParentID != *w.ParentID {
				return fmt.Errorf("workflow field input omitted its requested relationship change")
			}
			if step.Update == nil {
				continue
			}
			probe := ActionJournalRecord{ActionJournal: ActionJournal{OperationID: "probe-update", SessionID: a.SessionID, IssueID: step.IssueID, CarrierID: a.CarrierID, Action: "update", Phase: "prepared", Before: &before, UpdateIntent: step.Update}}
			if err := probe.validate(); err != nil {
				return err
			}
			after, err := step.Update.Target.contents()
			if err != nil {
				return err
			}
			after.UpdatedAt = before.UpdatedAt
			after.Revision, err = after.checksum()
			if err != nil {
				return err
			}
			probe.After = &after
			if matched, err := relationUndoScope(&probe); err != nil {
				return err
			} else if !matched {
				if err := scalarFieldUndoScope(&probe); err != nil {
					return err
				}
			}
			if step.Update.NativeEvents == nil || !reflect.DeepEqual(*step.Update.NativeEvents, w.Baselines[step.IssueID]) {
				return fmt.Errorf("workflow field input replaced its original event baseline")
			}
			for _, related := range step.Update.Related {
				index := slices.Index(ids, related.ID)
				if index < 0 || !reflect.DeepEqual(related.State, g.Members[index].Before) {
					return fmt.Errorf("workflow field input replaced an original graph observation")
				}
			}
		case i < minimum:
			if step.Kind != "workflow" || step.IssueID != w.Selected[i-fields] || step.Update != nil || step.Comment != nil {
				return fmt.Errorf("workflow request order differs from original selection")
			}
		default:
			if step.Kind != "comment" || step.IssueID != w.Selected[i-minimum] || step.Update != nil || step.Comment == nil {
				return fmt.Errorf("workflow companion differs from original selection")
			}
			activity, err := decodeActivity(apiComment{ID: 1, Body: step.Comment.Body}, step.IssueID)
			if err != nil || activity.Kind != "comment" || activity.SessionID != a.SessionID || step.Comment.OperationID == "" || activity.OperationID != step.Comment.OperationID || operations[step.Comment.OperationID] {
				return fmt.Errorf("workflow companion requires a distinct attributed input")
			}
			operations[step.Comment.OperationID] = true
		}
	}
	if err := validateWorkflowHandoffInputs(a, plan); err != nil {
		return err
	}
	if a.Phase != "prepared" && g.Forward != nil {
		states := map[string]ActionIssueState{}
		written := map[string]bool{}
		for _, effect := range g.Ordered.Forward {
			if effect.Record != nil {
				written[effect.Record.IssueID] = true
			}
		}
		for _, member := range g.Forward.Members {
			states[member.IssueID] = member.State
			if !written[member.IssueID] && !reflect.DeepEqual(g.Ordered.Events[member.IssueID], w.Baselines[member.IssueID]) {
				return fmt.Errorf("workflow forward result replaced an untouched event baseline")
			}
		}
		if !g.Forward.Partial {
			if err := workflowCommandComplete(&ActionJournalRecord{ActionJournal: a}, g.Ordered.Forward, states); err != nil {
				return err
			}
		}
	}
	return nil
}

func workflowEffectEvents(a *ActionJournal) *[]nativeStateEvent {
	if a.UpdateIntent != nil {
		return a.UpdateIntent.NativeEvents
	}
	return a.WorkflowEvents
}

func workflowEffectEventProof(a ActionJournal, events []nativeStateEvent) error {
	if a.UpdateIntent != nil {
		return updateEventProof(a, events)
	}
	if a.GroupContract != workflowUpdateContract || a.WorkflowEvents == nil || !reflect.DeepEqual(*a.WorkflowEvents, events) {
		return fmt.Errorf("workflow companion native event continuation changed")
	}
	return nil
}

func effectWasObserved(a *ActionJournalRecord) bool {
	return a.UpdateObservation != nil || a.CommentObservation != nil
}

func validateWorkflowUpdateMember(root, member *ActionJournalRecord) error {
	w := root.Group.WorkflowUpdates
	if member.GroupContract != workflowUpdateContract || member.WorkflowStep < 1 || member.WorkflowStep > len(w.Steps) {
		return fmt.Errorf("workflow member is outside declared command steps")
	}
	step := w.Steps[member.WorkflowStep-1]
	if step.Kind == "workflow" && member.Action == "comment_create" {
		handoff, exists := w.Handoffs[step.IssueID]
		if !exists || handoff.Create == nil || member.IssueID != step.IssueID || member.UpdateIntent != nil || member.WorkflowEvents == nil || !reflect.DeepEqual(member.CommentInput, handoff.Create) {
			return fmt.Errorf("workflow handoff differs from its frozen automatic creation input")
		}
		return member.validate()
	}
	if step.Kind == "comment" {
		if member.IssueID != step.IssueID || member.Action != "comment_create" || member.UpdateIntent != nil || !reflect.DeepEqual(member.CommentInput, step.Comment) || member.WorkflowEvents == nil {
			return fmt.Errorf("workflow companion differs from its original command input")
		}
		return member.validate()
	}
	if member.Action != "update" || member.UpdateIntent == nil || member.CommentInput != nil || member.WorkflowEvents != nil {
		return fmt.Errorf("workflow task effect requires a complete update input")
	}
	if step.Kind == "fields" {
		if step.Update == nil || step.IssueID != member.IssueID || !reflect.DeepEqual(member.UpdateIntent.Target, step.Update.Target) || !reflect.DeepEqual(member.UpdateIntent.NativeEvents, step.Update.NativeEvents) || len(member.UpdateIntent.Related) != len(step.Update.Related) {
			return fmt.Errorf("workflow fields replaced their frozen input")
		}
		for i, related := range step.Update.Related {
			if member.UpdateIntent.Related[i].ID != related.ID {
				return fmt.Errorf("workflow fields replaced original related membership")
			}
		}
		return member.validate()
	}
	before, err := actionStateRecord(member.Before, member.IssueID)
	if err != nil {
		return err
	}
	after, err := member.UpdateIntent.Target.contents()
	if err != nil {
		return err
	}
	after.UpdatedAt = member.Before.UpdatedAt
	after.Revision, err = after.checksum()
	if err != nil {
		return err
	}
	probe := *member
	probe.After = &after
	records, err := workflowOriginalRecords(root.Group)
	if err != nil {
		return err
	}
	// Graph-only requested states scope cascades after the field phase. They
	// never authorize a target revision or substitute for an actual predecessor.
	g := &ActionGroup{Roots: []string{step.IssueID}}
	for _, m := range root.Group.Members {
		state := m.Before
		for _, field := range w.Steps {
			if field.Kind == "fields" && field.IssueID == m.IssueID && field.Update != nil {
				state, err = field.Update.Target.contents()
				if err != nil {
					return err
				}
				state.UpdatedAt = m.Before.UpdatedAt
				state.Revision, err = state.checksum()
				if err != nil {
					return err
				}
			}
		}
		g.Members = append(g.Members, ActionGroupMember{IssueID: m.IssueID, Before: state})
	}
	rootBefore := records[step.IssueID]
	if member.IssueID == step.IssueID {
		rootBefore = *before
	}
	action, err := workflowUpdateRequest(rootBefore.Status, w.Status, w.Options)
	if err != nil {
		return err
	}
	g.Command, g.Cascade = action, action == "close" || action == "approve" && !w.Options.RecordOnly
	plan, err := w.graph(records)
	if err != nil || !slices.Contains(plan.AfterScopes[step.IssueID], member.IssueID) {
		return fmt.Errorf("workflow transition escaped its original potential cascade scope")
	}
	matched := false
	switch {
	case nativeGroupCommand(action):
		matched, err = nativeGroupMemberScope(g, &probe)
	case action == "review":
		matched, err = reviewGroupMemberScope(g, &probe)
	case action == "reject":
		matched, err = reviewTransitionUndoScope(&probe)
	default:
		matched, err = workflowUndoScope(&probe)
	}
	if err != nil || !matched {
		return &UndoScopeError{Reason: "workflow effect differs from its declared transition and cascade scope"}
	}
	_, meta, _, err := decodeBody(after.Body)
	if err != nil || meta.Details == nil || len(meta.Details.Transitions) == 0 {
		return fmt.Errorf("workflow effect omitted its transition attribution")
	}
	tr := meta.Details.Transitions[len(meta.Details.Transitions)-1]
	if member.IssueID == step.IssueID {
		if tr.Action != action || tr.Reason != w.Options.Reason || tr.AdminReason != w.Options.AdminReason || tr.SelfCloseException != w.Options.SelfCloseException {
			return fmt.Errorf("workflow root transition replaced its requested action or reason")
		}
		if groupBasicCommand(action) {
			_, _, err := basicTransitionDecision(before, action, w.Options)
			if err != nil {
				return err
			}
			var snapshot *models.GitSnapshot
			if w.Options.Snapshot != nil {
				copy := *w.Options.Snapshot
				copy.IssueID, copy.Event = member.IssueID, action
				snapshot = &copy
			}
			if !reflect.DeepEqual(snapshot, tr.Snapshot) {
				return fmt.Errorf("workflow root replaced its requested Git snapshot")
			}
		}
		if action == "review" && meta.Details.Minor != (before.Minor || w.Options.Minor) {
			return fmt.Errorf("workflow root replaced its requested review exemption")
		}
		if action == "approve" {
			bd, err := before.CopyDetails()
			if err != nil {
				return err
			}
			if w.Options.RecordOnly != (meta.Details.Status == models.StatusInReview) {
				return fmt.Errorf("workflow approval changed record-only input")
			}
			if len(meta.Details.Reviews) > len(bd.Reviews) {
				decision := reviewerEligibility(before, bd, w.Options)
				verdict := w.Options.Decision
				if verdict == "" {
					verdict = reviewpolicy.DecisionApproved
				}
				actual := meta.Details.Reviews[len(meta.Details.Reviews)-1]
				if !decision.Allowed || actual.Decision != verdict || actual.SelfReview != decision.SelfReview || actual.ReviewedBy != decision.AttributedTo {
					return fmt.Errorf("workflow approval replaced original review options or eligibility")
				}
			} else if active := activeApproval(&bd); active != nil {
				if err := validateRecordedClose(active, w.Options); err != nil {
					return err
				}
			}
		}
	} else if !g.Cascade && action != "review" {
		return fmt.Errorf("workflow request has an undeclared cascade")
	} else if action == "review" && tr.Action == "review" {
		// Siblings are watched for parent completion, but are not descendant
		// review targets. A watched revision alone cannot authorize a write.
		parents := map[string]string{}
		for _, m := range g.Members {
			r, err := actionStateRecord(&m.Before, m.IssueID)
			if err != nil {
				return err
			}
			parents[m.IssueID] = r.ParentID
		}
		from, want := member.IssueID, step.IssueID
		aggregate, err := autoParentReviewUndoScope(&probe)
		if err != nil {
			return err
		}
		if aggregate {
			from, want = step.IssueID, member.IssueID
		}
		seen := map[string]bool{}
		ancestor := parents[from]
		for ancestor != "" && ancestor != want && !seen[ancestor] {
			seen[ancestor] = true
			ancestor = parents[ancestor]
		}
		if ancestor != want || meta.Details.Minor != before.Minor || meta.Details.ReviewHandoffID != "" || !aggregate && tr.Reason != "Cascaded review from "+step.IssueID {
			return fmt.Errorf("workflow descendant review escaped the requested root")
		}
	}
	return member.validate()
}

func validateWorkflowUpdateBindings(root, member *ActionJournalRecord, expected map[string]ActionIssueState) error {
	if member.UpdateIntent != nil {
		for _, related := range member.UpdateIntent.Related {
			state, exists := expected[related.ID]
			if !exists || !reflect.DeepEqual(state, related.State) {
				return fmt.Errorf("workflow related observation is not an actual command predecessor")
			}
		}
	}
	return nil
}

func validateWorkflowStepOrder(root *ActionJournalRecord, attempts []*ActionJournalRecord) error {
	seen := map[int]bool{}
	handoffs := map[int]bool{}
	previous := 0
	for _, member := range attempts {
		if member.WorkflowStep < previous || member.WorkflowStep < 1 || member.WorkflowStep > len(root.Group.WorkflowUpdates.Steps) {
			return fmt.Errorf("workflow effect reordered its original command steps")
		}
		for index, step := range root.Group.WorkflowUpdates.Steps[:member.WorkflowStep-1] {
			if (step.Update != nil || step.Comment != nil) && !seen[index+1] {
				return fmt.Errorf("workflow effect skipped an earlier declared field or comment input")
			}
		}
		step := root.Group.WorkflowUpdates.Steps[member.WorkflowStep-1]
		if step.Kind == "workflow" && member.Action == "comment_create" {
			if handoffs[member.WorkflowStep] || seen[member.WorkflowStep] {
				return fmt.Errorf("workflow automatic handoff was repeated or reordered after review")
			}
			handoffs[member.WorkflowStep] = true
		}
		if seen[member.WorkflowStep] && step.Kind != "workflow" {
			return fmt.Errorf("workflow field/comment input was attempted twice")
		}
		seen[member.WorkflowStep], previous = true, member.WorkflowStep
	}
	return nil
}

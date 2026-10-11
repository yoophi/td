package ghstore

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/marcus/td/internal/models"
)

// OrderedGroupUndoStep separates an actual source observation, a requested
// compensation and its eventual result. A pending input cannot authorize the
// next step. These records are not ordinary causal undo receipts.
type OrderedGroupUndoStep struct {
	Comment              *ActionCommentState `json:"comment,omitempty"`
	GroupOperationID     string              `json:"group_operation_id"`
	GroupUndoOperationID string              `json:"group_undo_operation_id"`
	ForwardReceiptID     string              `json:"forward_receipt_id"`
	OperationID          string              `json:"operation_id"`
	SessionID            string              `json:"session_id"`
	Actor                string              `json:"actor"`
	At                   time.Time           `json:"at"`
	Source               ActionIssueState    `json:"source"`
	Input                ActionUpdateInput   `json:"input"`
	Baseline             []nativeStateEvent  `json:"baseline"`
	RequiresNewReview    bool                `json:"requires_new_review"`
	Result               *ActionIssueState   `json:"result,omitempty"`
	EventsAfter          *[]nativeStateEvent `json:"events_after,omitempty"`
	Proof                string              `json:"proof,omitempty"`
	ObserverSession      string              `json:"observer_session,omitempty"`
	ObservedAt           time.Time           `json:"observed_at,omitempty"`
}

func orderedUndoIdentity(op, session, id string) error {
	return (ActionJournal{OperationID: op, SessionID: session, IssueID: id, Action: "create", Phase: "prepared"}).validate()
}

// Build compensation from immutable former fields and the actual current
// audit. A previous inverse's audit is preserved, never rewound to an old body.
func orderedEffectCompensation(effect *ActionJournalRecord, source ActionIssueState, groupUndo, op, session, actor string, at time.Time) (*OrderedGroupUndoStep, error) {
	if effect == nil || effect.Before == nil || effect.After == nil || effect.Phase != "applied" || at.IsZero() || at.Before(source.UpdatedAt) || op == effect.OperationID || op == groupUndo || !strings.EqualFold(effect.Author, actor) || effect.SessionID != session {
		return nil, fmt.Errorf("ordered compensation requires attributed actual input and a distinct inverse identity")
	}
	if err := source.validate(); err != nil {
		return nil, err
	}
	if err := orderedUndoIdentity(op, session, effect.IssueID); err != nil {
		return nil, err
	}
	if effect.Action == "comment_create" && effect.GroupContract == workflowUpdateContract {
		if effect.CommentAfter == nil || effect.CommentInput == nil {
			return nil, fmt.Errorf("workflow compensation omitted its exact companion result")
		}
		comment := *effect.CommentAfter
		if err := comment.validate(); err != nil {
			return nil, err
		}
		return &OrderedGroupUndoStep{Comment: &comment, GroupOperationID: effect.GroupOperationID, GroupUndoOperationID: groupUndo, ForwardReceiptID: effect.ID, OperationID: op, SessionID: session, Actor: actor, At: at, Source: source, Input: ActionUpdateInput{Title: source.Title, Body: source.Body, State: source.State, Labels: slices.Clone(source.Labels)}}, nil
	}
	desc, original, _, err := decodeBody(effect.Before.Body)
	if err != nil {
		return nil, err
	}
	_, current, _, err := decodeBody(source.Body)
	if err != nil || effect.GroupContract != workflowUpdateContract && (current.Details == nil || len(current.Details.Transitions) == 0) {
		return nil, fmt.Errorf("ordered compensation requires observed forward audit")
	}
	// JSON copying prevents review supersession from modifying original receipts
	// or the caller's actual source observation through shared slice pointers.
	data, err := json.Marshal(current.Details)
	if err != nil {
		return nil, err
	}
	var audit IssueDetails
	if err := json.Unmarshal(data, &audit); err != nil {
		return nil, err
	}
	for _, tr := range audit.Transitions {
		if tr.OperationID == op || tr.OperationID == groupUndo || at.Before(tr.At) {
			return nil, fmt.Errorf("ordered compensation reuses an audit identity or reverses audit time")
		}
	}
	_, forward, _, err := decodeBody(effect.After.Body)
	if err != nil || effect.GroupContract != workflowUpdateContract && (forward.Details == nil || len(forward.Details.Transitions) == 0) {
		return nil, fmt.Errorf("ordered compensation requires its original writer transition")
	}
	originalOperation := effect.OperationID
	if forward.Details != nil && len(forward.Details.Transitions) != 0 && effect.GroupContract != workflowUpdateContract {
		originalOperation = forward.Details.Transitions[len(forward.Details.Transitions)-1].OperationID
	}
	d := IssueDetails{Status: models.Status(effect.Before.State)}
	if original.Details != nil {
		data, err := json.Marshal(original.Details)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &d); err != nil {
			return nil, err
		}
		if d.Status == "" {
			d.Status = models.Status(effect.Before.State)
		}
	}
	requires := !d.Minor && (d.ReviewBasis != "" || audit.ReviewBasis != "" || activeApproval(&d) != nil || activeApproval(&audit) != nil)
	d.Reviews, d.Sessions, d.Transitions = audit.Reviews, audit.Sessions, audit.Transitions
	supersedeReviews(&d, at)
	d.ReviewBasis, d.ReviewEvents, d.ReviewRequestedBySession = "", "", ""
	d.ReviewHandoffID, d.ReviewHandoffUpdatedAt = "", time.Time{}
	d.ClosedBySession = ""
	if effect.Before.State == "closed" {
		d.ClosedBySession = session
	}
	reason := "Group undo compensation for " + effect.ID + " (" + originalOperation + "); forward and inverse audit retained, previous approvals invalidated"
	d.Transitions = append(d.Transitions, TransitionRecord{OperationID: op, Action: "undo_group", From: nativeUndoStatus(current, source.State), To: d.Status, SessionID: session, Reason: reason, At: at})
	original.Details, original.LastStateReason = &d, reason
	body, err := encodeBody(desc, original)
	if err != nil {
		return nil, err
	}
	return &OrderedGroupUndoStep{GroupOperationID: effect.GroupOperationID, GroupUndoOperationID: groupUndo, ForwardReceiptID: effect.ID, OperationID: op, SessionID: session, Actor: actor, At: at, Source: source, Input: ActionUpdateInput{Title: effect.Before.Title, Body: body, State: effect.Before.State, Labels: slices.Clone(effect.Before.Labels)}, RequiresNewReview: requires}, nil
}

func orderedStepEventProof(step *OrderedGroupUndoStep, events []nativeStateEvent, completed bool) error {
	if step.Baseline == nil || events == nil {
		return fmt.Errorf("ordered compensation needs explicit actual event observations")
	}
	last := int64(0)
	for _, event := range step.Baseline {
		if event.ID <= last || event.Event != "closed" && event.Event != "reopened" {
			return fmt.Errorf("ordered compensation has an invalid event baseline")
		}
		last = event.ID
	}
	if !completed || step.Source.State == step.Input.State {
		if !reflect.DeepEqual(step.Baseline, events) {
			return fmt.Errorf("ordered compensation event prefix changed")
		}
		return nil
	}
	kind := "closed"
	if step.Input.State == "open" {
		kind = "reopened"
	}
	if len(events) != len(step.Baseline)+1 || !reflect.DeepEqual(step.Baseline, events[:len(step.Baseline)]) || events[len(step.Baseline)].ID <= last || events[len(step.Baseline)].Event != kind {
		return fmt.Errorf("ordered compensation native inverse event is missing or changed")
	}
	return nil
}

// PlanOrderedGroupUndoStep validates the entire original sequence and every
// completed compensation before admitting one new inverse input. It performs
// no I/O. The executor must independently fence actual records, events, group
// membership and exact native receipts immediately before writes. Completion
// observations can continue this one frozen group, never an unrelated group or
// an ordinary causal undo chain. Weak observation provenance remains unchanged.
func PlanOrderedGroupUndoStep(group *ActionJournalRecord, effects []*ActionJournalRecord, forwardEvents map[string][]nativeStateEvent, completed []*OrderedGroupUndoStep, source ActionIssueState, events []nativeStateEvent, groupUndo, op, session, actor string, at time.Time) (*OrderedGroupUndoStep, error) {
	if group == nil || group.Group == nil || session != group.SessionID || !strings.EqualFold(actor, group.Author) || groupUndo == group.OperationID || group.UndoOperationID != "" && group.UndoOperationID != groupUndo {
		return nil, fmt.Errorf("ordered undo requires the original group session, author and inverse identity")
	}
	if err := orderedUndoIdentity(groupUndo, session, group.IssueID); err != nil {
		return nil, err
	}
	chains, err := orderedGroupEffects(group, effects)
	if err != nil {
		return nil, err
	}
	ordered := slices.Clone(effects)
	slices.SortFunc(ordered, func(a, b *ActionJournalRecord) int {
		an, _ := CommentNumber(a.ID)
		bn, _ := CommentNumber(b.ID)
		if an < bn {
			return 1
		}
		if an > bn {
			return -1
		}
		return 0
	})
	expected := map[string]ActionIssueState{}
	expectedEvents := map[string][]nativeStateEvent{}
	used := map[string]bool{group.OperationID: true, groupUndo: true}
	for _, effect := range ordered {
		if effect.Phase != "applied" {
			return nil, fmt.Errorf("uncertain or individually inverted forward effects cannot authorize ordered compensation")
		}
		if effect.OperationID == groupUndo {
			return nil, fmt.Errorf("ordered group inverse reuses a forward identity")
		}
		used[effect.OperationID] = true
	}
	for id, chain := range chains {
		last := chain[len(chain)-1]
		expected[id] = *last.After
		baseline, found := forwardEvents[id]
		if !found || baseline == nil {
			return nil, fmt.Errorf("ordered undo omitted its frozen forward event observation")
		}
		if workflowEffectEvents(&last.ActionJournal) != nil {
			if err := workflowEffectEventProof(last.ActionJournal, baseline); err != nil {
				return nil, err
			}
		}
		if err := orderedStepEventProof(&OrderedGroupUndoStep{Baseline: baseline}, baseline, false); err != nil {
			return nil, err
		}
		expectedEvents[id] = baseline
	}
	for _, effect := range ordered {
		if workflowEffectEvents(&effect.ActionJournal) == nil {
			continue
		}
		actual := forwardEvents[effect.IssueID]
		before := *workflowEffectEvents(&effect.ActionJournal)
		end := len(before)
		native := effect.Before.State != effect.After.State
		if native {
			end++
		}
		if len(actual) < end {
			return nil, fmt.Errorf("ordered original forward event prefix is missing")
		}
		prefix := actual[:end]
		if err := workflowEffectEventProof(effect.ActionJournal, prefix); err != nil {
			return nil, err
		}
		if native {
			if err := nativeUndoHistoricalReview(effect, &ActionNativeUndo{Baseline: prefix}); err != nil {
				return nil, err
			}
		} else {
			original, err := actionStateRecord(effect.Before, effect.IssueID)
			if err != nil {
				return nil, err
			}
			details, err := original.CopyDetails()
			if err != nil {
				return nil, err
			}
			if details.ReviewBasis != "" && (!group.Group.workflowUpdateGroup() || effect.GroupPreviousReceiptID == "") && (details.ReviewBasis != reviewBasis(original, details) || details.ReviewEvents != stateEventDigest(prefix)) {
				return nil, fmt.Errorf("ordered original review checkpoint cannot be verified")
			}
		}
	}
	if len(completed) >= len(ordered) {
		return nil, fmt.Errorf("ordered group has no remaining forward effect to compensate")
	}
	for i, step := range completed {
		effect := ordered[i]
		if step == nil || step.ForwardReceiptID != effect.ID || step.GroupOperationID != group.OperationID || step.GroupUndoOperationID != groupUndo || step.Source.Revision != expected[effect.IssueID].Revision || used[step.OperationID] || !reflect.DeepEqual(step.Baseline, expectedEvents[effect.IssueID]) {
			return nil, fmt.Errorf("completed compensation differs from its frozen group sequence")
		}
		want, err := orderedEffectCompensation(effect, step.Source, groupUndo, step.OperationID, session, actor, step.At)
		if err != nil {
			return nil, err
		}
		if step.Actor != want.Actor || step.SessionID != want.SessionID || step.RequiresNewReview != want.RequiresNewReview || !reflect.DeepEqual(step.Input, want.Input) || !reflect.DeepEqual(step.Comment, want.Comment) || step.Result == nil || step.EventsAfter == nil {
			return nil, fmt.Errorf("compensation has no complete original plan and result")
		}
		if err := validateOrderedStepResult(step); err != nil {
			return nil, err
		}

		used[step.OperationID] = true
		expected[effect.IssueID] = *step.Result
		expectedEvents[effect.IssueID] = *step.EventsAfter
	}
	effect := ordered[len(completed)]
	if used[op] || source.Revision != expected[effect.IssueID].Revision || !reflect.DeepEqual(events, expectedEvents[effect.IssueID]) {
		return nil, fmt.Errorf("ordered compensation source, events or identity changed")
	}
	step, err := orderedEffectCompensation(effect, source, groupUndo, op, session, actor, at)
	if err != nil {
		return nil, err
	}
	step.Baseline = slices.Clone(events)
	if err := orderedStepEventProof(step, events, false); err != nil {
		return nil, err
	}
	return step, nil
}

func validateOrderedStepResult(step *OrderedGroupUndoStep) error {
	if step == nil || step.Result == nil || step.EventsAfter == nil {
		return fmt.Errorf("compensation has no complete result")
	}
	if err := step.Result.validate(); err != nil {
		return err
	}
	contents, err := step.Input.contents()
	if err != nil || !sameActionContents(contents, *step.Result) || step.Result.UpdatedAt.Before(step.Source.UpdatedAt) {
		return fmt.Errorf("compensation result differs from its recorded input")
	}
	if err := orderedStepEventProof(step, *step.EventsAfter, true); err != nil {
		return err
	}
	switch step.Proof {
	case "verified-group-inverse":
		if step.ObserverSession != "" || !step.ObservedAt.IsZero() {
			return fmt.Errorf("verified compensation cannot invent observer provenance")
		}
	case inverseObservationProof:
		if err := orderedUndoIdentity("check-observer", step.ObserverSession, "gh-1"); err != nil || step.ObservedAt.IsZero() || step.ObservedAt.Before(step.At) {
			return fmt.Errorf("observed compensation needs its separate provenance")
		}
	default:
		return fmt.Errorf("pending compensation cannot authorize the next inverse")
	}
	return nil
}

func sameOrderedStepIntent(a, b *OrderedGroupUndoStep) bool {
	if a == nil || b == nil {
		return a == b
	}
	left, right := *a, *b
	left.Result, right.Result = nil, nil
	left.EventsAfter, right.EventsAfter = nil, nil
	left.Proof, right.Proof = "", ""
	left.ObserverSession, right.ObserverSession = "", ""
	left.ObservedAt, right.ObservedAt = time.Time{}, time.Time{}
	return reflect.DeepEqual(left, right)
}

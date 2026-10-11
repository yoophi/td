package ghstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/marcus/td/internal/models"
)

// Inverse inputs have no invented native clocks. Started is a durable prefix:
// one started step may lack its result after an uncertain PATCH. Observation
// may fill that result, but must never replay the pending target write.
type ActionImportInverse struct {
	At       time.Time                   `json:"at"`
	Watches  []ActionRelatedObservation  `json:"watches"`
	Steps    []ImportInverseStep         `json:"steps"`
	Started  int                         `json:"started"`
	Results  []ImportInverseResult       `json:"results"`
	Result   *CreationObservation        `json:"result,omitempty"`
	Canceled []ImportInverseCancellation `json:"canceled,omitempty"`
}

// A cancellation attests that the original executor returned before calling
// PATCH. It cannot be inferred by a later observer from unchanged task data.
type ImportInverseCancellation struct {
	Step        int                 `json:"step"`
	Stage       string              `json:"stage"`
	Observation CreationObservation `json:"observation"`
}

type ImportInverseStep struct {
	Member int               `json:"member"`
	Input  ActionUpdateInput `json:"input"`
}

type ImportInverseResult struct {
	State       ImportNativeState   `json:"state"`
	Observation CreationObservation `json:"observation"`
}

func importNativeInput(native apiIssue) (ActionUpdateInput, error) {
	record, err := native.record()
	if err != nil {
		return ActionUpdateInput{}, err
	}
	state, err := actionIssueState(record)
	if err != nil {
		return ActionUpdateInput{}, err
	}
	input, err := updateInputFromPayload(state, nil)
	slices.Sort(input.Labels)
	return input, err
}

func importInputNative(native apiIssue, input ActionUpdateInput) apiIssue {
	native.Title, native.Body, native.State = input.Title, input.Body, input.State
	native.Labels = nil
	for _, label := range input.Labels {
		native.Labels = append(native.Labels, struct {
			Name string `json:"name"`
		}{Name: label})
	}
	return native
}

// Derive the complete inverse from immutable before/forward evidence. Existing
// tasks regain their own fields and audit; previous approvals are invalidated.
// New allocations retain their identity, native state and comments and become
// logically deleted. Source history never becomes active review authority.
func importCompensationInput(a ActionJournal, n int, undo string, at time.Time) (ActionUpdateInput, error) {
	member, forward := a.ImportCommand.Members[n], a.ImportForward.Targets[n]
	native := forward.Issue
	if member.Action == "skip" {
		return importNativeInput(native)
	}
	if member.Before != nil {
		native = member.Before.Issue
	}
	description, meta, _, err := decodeBody(native.Body)
	if err != nil {
		return ActionUpdateInput{}, err
	}
	// Metadata contains slices/pointers shared with archived evidence.
	data, err := json.Marshal(meta)
	if err != nil {
		return ActionUpdateInput{}, err
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return ActionUpdateInput{}, err
	}
	details := IssueDetails{Status: models.Status(native.State)}
	if meta.Details != nil {
		details = *meta.Details
	}
	if details.Status == "" {
		details.Status = models.Status(native.State)
	}
	current, err := forward.Issue.record()
	if err != nil {
		return ActionUpdateInput{}, err
	}
	action := "undo_group"
	if member.Before == nil {
		if details.DeletedAt != nil {
			return importNativeInput(native)
		}
		details.DeletedAt = &at
		action = "delete"
	} else {
		supersedeReviews(&details, at)
		details.ReviewBasis, details.ReviewEvents, details.ReviewRequestedBySession = "", "", ""
		details.ReviewHandoffID, details.ReviewHandoffUpdatedAt = "", time.Time{}
		details.ClosedBySession = ""
		if native.State == "closed" {
			details.ClosedBySession = a.SessionID
		}
	}
	reason := "Undo import " + a.OperationID + "; native identity/history retained and previous approvals invalidated"
	details.Transitions = append(details.Transitions, TransitionRecord{OperationID: undo, Action: action, From: current.Status, To: details.Status, SessionID: a.SessionID, Reason: reason, At: at})
	if err := details.validate(); err != nil {
		return ActionUpdateInput{}, err
	}
	meta.Details = &details
	native.Body, err = encodeBody(description, meta)
	if err != nil {
		return ActionUpdateInput{}, err
	}
	return importNativeInput(native)
}

// Planning shares the forward import graph-order/detachment checks. Only the
// selected original members can become steps; reachable outside tasks are
// frozen watches. Unrelated tasks are neither modified nor frozen wholesale.
func planImportInverse(a ActionJournal, snapshot *Snapshot, undo string, at time.Time) (*ActionImportInverse, error) {
	if a.ImportCommand == nil || a.ImportForward == nil || snapshot == nil {
		return nil, fmt.Errorf("import inverse requires completed forward evidence and a fresh graph")
	}
	probe := ActionJournal{OperationID: undo, SessionID: a.SessionID, IssueID: "gh-1", Action: "create", Phase: "prepared"}
	if err := probe.validate(); err != nil || undo == a.OperationID || at.IsZero() {
		return nil, fmt.Errorf("import inverse requires a new operation and timestamp")
	}
	result := &ActionImportInverse{At: at, Watches: []ActionRelatedObservation{}, Steps: []ImportInverseStep{}, Results: []ImportInverseResult{}}
	final, selected := map[string]Record{}, map[string]bool{}
	plan := &ImportPlan{Repository: a.ImportCommand.Repository, snapshot: &Snapshot{fullHistory: true}}
	inputs := make([]ActionUpdateInput, len(a.ImportCommand.Members))
	relations := map[string]importRelations{}
	all := map[string]Record{}
	for _, record := range snapshot.Issues(true) {
		all[record.ID] = record
	}
	for n, member := range a.ImportCommand.Members {
		forward := a.ImportForward.Targets[n]
		if at.Before(forward.Issue.UpdatedAt) {
			return nil, fmt.Errorf("import inverse timestamp predates its forward observation")
		}
		input, err := importCompensationInput(a, n, undo, at)
		if err != nil {
			return nil, err
		}
		inputs[n] = input
		current, err := forward.Issue.record()
		if err != nil {
			return nil, err
		}
		all[current.ID], selected[current.ID] = *current, true
		projected, err := importInputNative(forward.Issue, input).record()
		if err != nil {
			return nil, err
		}
		final[current.ID] = *projected
		dependencies := []string{}
		if projected.Details != nil {
			dependencies = append(dependencies, projected.Details.Dependencies...)
		}
		slices.Sort(dependencies)
		relations[member.SourceKey] = importRelations{parent: projected.ParentID, dependencies: dependencies}
		action := "overwrite"
		if member.Action == "skip" {
			action = "skip"
		}
		plan.Rows = append(plan.Rows, ImportPlanRow{SourceKey: member.SourceKey, TargetID: current.ID, Action: action})
	}
	for id, record := range all {
		plan.snapshot.records = append(plan.snapshot.records, record)
		if !selected[id] {
			final[id] = record
		}
	}
	// An outside task must not silently acquire a deleted parent/dependency.
	// This is repeated against the complete live graph by the executor.
	for id, record := range all {
		if selected[id] || record.DeletedAt != nil {
			continue
		}
		edges := []string{record.ParentID}
		if record.Details != nil {
			edges = append(edges, record.Details.Dependencies...)
		}
		for _, target := range edges {
			if selected[target] && final[target].DeletedAt != nil {
				return nil, fmt.Errorf("import inverse would delete an outside task's relationship: %w", &ConflictError{ID: id})
			}
		}
	}
	seen := map[string]bool{}
	var visit func(string) error
	visit = func(id string) error {
		if id == "" || seen[id] {
			return nil
		}
		seen[id] = true
		record, ok := final[id]
		if !ok {
			return fmt.Errorf("import inverse relationship target disappeared: %s", id)
		}
		if !selected[id] {
			state, err := actionIssueState(&record)
			if err != nil {
				return err
			}
			result.Watches = append(result.Watches, ActionRelatedObservation{ID: id, State: state})
		}
		if err := visit(record.ParentID); err != nil {
			return err
		}
		if record.Details != nil {
			for _, dependency := range record.Details.Dependencies {
				if err := visit(dependency); err != nil {
					return err
				}
			}
		}
		return nil
	}
	for _, row := range plan.Rows {
		if err := visit(row.TargetID); err != nil {
			return nil, err
		}
	}
	slices.SortFunc(result.Watches, func(a, b ActionRelatedObservation) int { return strings.Compare(a.ID, b.ID) })
	order, err := importGraphOrder(plan, relations)
	var detach *importDetachRequiredError
	detachedMembers := map[int]bool{}
	if errors.As(err, &detach) {
		order, err = importDetachedGraphOrder(plan, relations)
		if err != nil {
			return nil, err
		}
		for n, row := range plan.Rows {
			if row.Action == "skip" {
				continue
			}
			native := a.ImportForward.Targets[n].Issue
			description, meta, _, err := decodeBody(native.Body)
			if err != nil {
				return nil, err
			}
			if meta.Details == nil || meta.Details.ParentID == "" && len(meta.Details.Dependencies) == 0 {
				continue
			}
			meta.Details.ParentID, meta.Details.Dependencies = "", nil
			native.Body, err = encodeBody(description, meta)
			if err != nil {
				return nil, err
			}
			input, err := importNativeInput(native)
			if err != nil {
				return nil, err
			}
			result.Steps = append(result.Steps, ImportInverseStep{Member: n, Input: input})
			detachedMembers[n] = true
		}
	} else if err != nil {
		return nil, err
	}
	for _, n := range order {
		before, err := importNativeInput(a.ImportForward.Targets[n].Issue)
		if err != nil {
			return nil, err
		}
		if detachedMembers[n] || !reflect.DeepEqual(before, inputs[n]) {
			result.Steps = append(result.Steps, ImportInverseStep{Member: n, Input: inputs[n]})
		}
	}
	return result, nil
}

func (a ActionJournal) validateImportInverse() error {
	i := a.ImportInverse
	inverse := a.Phase == "undo_prepared" || a.Phase == "undone"
	if inverse != (i != nil) || inverse != (a.UndoOperationID != "") {
		return fmt.Errorf("import inverse requires its declared phase and undo identity")
	}
	if i == nil {
		return nil
	}
	if i.Steps == nil || i.Results == nil || i.Watches == nil || i.Started < len(i.Results) || i.Started > len(i.Results)+1 || i.Started > len(i.Steps) {
		return fmt.Errorf("import inverse has invalid ordered progress")
	}
	for n, cancellation := range i.Canceled {
		if cancellation.Step < 0 || cancellation.Step >= len(i.Steps) || cancellation.Step > len(i.Results) || n > 0 && cancellation.Step < i.Canceled[n-1].Step || cancellation.Stage != "snapshot" && cancellation.Stage != "target" && cancellation.Stage != "conflict" {
			return fmt.Errorf("invalid import inverse pre-write cancellation")
		}
		if cancellation.Observation.Proof != "verified" {
			return fmt.Errorf("import inverse cancellation requires original executor evidence")
		}
		if err := validateImportInverseObservation(a, cancellation.Observation); err != nil {
			return err
		}
	}
	snapshot := &Snapshot{fullHistory: true}
	seen := map[string]bool{}
	for _, target := range a.ImportForward.Targets {
		record, err := target.Issue.record()
		if err != nil {
			return err
		}
		snapshot.records = append(snapshot.records, *record)
		seen[record.ID] = true
	}
	for _, watch := range i.Watches {
		if seen[watch.ID] || watch.ID == a.CarrierID {
			return fmt.Errorf("import inverse has duplicate or selected graph watches")
		}
		if err := watch.State.validate(); err != nil {
			return err
		}
		record, err := actionStateRecord(&watch.State, watch.ID)
		if err != nil {
			return err
		}
		seen[watch.ID] = true
		snapshot.records = append(snapshot.records, *record)
	}
	expected, err := planImportInverse(a, snapshot, a.UndoOperationID, i.At)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(expected.Steps, i.Steps) || !reflect.DeepEqual(expected.Watches, i.Watches) {
		return fmt.Errorf("import inverse differs from its original member projections or graph")
	}
	states := slices.Clone(a.ImportForward.Targets)
	for n, result := range i.Results {
		step := i.Steps[n]
		before := states[step.Member]
		if err := validateImportInverseResult(a, before, step, result); err != nil {
			return err
		}
		states[step.Member] = result.State
	}
	if (a.Phase == "undone") != (i.Result != nil) {
		return fmt.Errorf("import inverse completion and command phase disagree")
	}
	if i.Result != nil {
		if i.Started != len(i.Steps) || len(i.Results) != len(i.Steps) {
			return fmt.Errorf("import inverse completed before every step result")
		}
		if err := validateImportInverseObservation(a, *i.Result); err != nil {
			return err
		}
		for _, result := range i.Results {
			if i.Result.Proof == "verified" && result.Observation.Proof != "verified" {
				return fmt.Errorf("import inverse cannot upgrade an observed step to verified")
			}
		}
	}
	return nil
}

func validateImportInverseObservation(a ActionJournal, observation CreationObservation) error {
	probe := ActionJournal{OperationID: "inverse-observer", SessionID: observation.SessionID, IssueID: "gh-1", Action: "create", Phase: "prepared"}
	if err := probe.validate(); err != nil || observation.Actor == "" || observation.ObservedAt.Before(a.ImportInverse.At) || observation.Proof != "verified" && observation.Proof != "observed" || observation.Proof == "verified" && observation.SessionID != a.SessionID {
		return fmt.Errorf("invalid import inverse observation attribution")
	}
	return nil
}

func validateImportInverseResult(a ActionJournal, before ImportNativeState, step ImportInverseStep, result ImportInverseResult) error {
	actual := result.State
	id := fmt.Sprintf("gh-%d", before.Issue.Number)
	if err := actual.validate(a.ImportCommand.Repository, id); err != nil {
		return err
	}
	input, err := importNativeInput(actual.Issue)
	if err != nil || !reflect.DeepEqual(input, step.Input) || actual.Issue.User != before.Issue.User || !actual.Issue.CreatedAt.Equal(before.Issue.CreatedAt) || actual.Issue.URL != before.Issue.URL || actual.Issue.UpdatedAt.Before(before.Issue.UpdatedAt) || !reflect.DeepEqual(actual.Comments, before.Comments) {
		return fmt.Errorf("import inverse result changed native identity/history or differs from its input")
	}
	probe := ActionJournal{Before: &ActionIssueState{State: before.Issue.State}, UpdateIntent: &ActionUpdateIntent{Target: step.Input, NativeEvents: &before.Events}}
	if err := updateEventProof(probe, actual.Events); err != nil {
		return err
	}
	return validateImportInverseObservation(a, result.Observation)
}

func validateImportInverseNative(a ActionJournal, root apiComment) error {
	if a.ImportInverse == nil {
		return nil
	}
	for _, result := range a.ImportInverse.Results {
		if !strings.EqualFold(result.Observation.Actor, root.User.Login) || result.State.Issue.UpdatedAt.After(root.UpdatedAt) {
			return fmt.Errorf("import inverse observation differs from its native root author or clock")
		}
	}
	for _, cancellation := range a.ImportInverse.Canceled {
		if !strings.EqualFold(cancellation.Observation.Actor, root.User.Login) {
			return fmt.Errorf("import inverse cancellation differs from its native author")
		}
	}
	if result := a.ImportInverse.Result; result != nil && !strings.EqualFold(result.Actor, root.User.Login) {
		return fmt.Errorf("import inverse completion author differs from its native root")
	}
	return nil
}

// Result evidence is append-only. Each target write requires a new Started
// marker first; receiving/reconciling its outcome cannot start the next step.
func validateImportInverseRewrite(old ActionJournal, phase, undo string, next *ActionImportInverse) error {
	prior := old.ImportInverse
	if old.Phase == phase && old.UndoOperationID == undo && reflect.DeepEqual(prior, next) {
		return nil
	}
	if prior == nil {
		if old.Phase == "prepared" && phase == "applied" && next == nil && undo == "" {
			return nil
		}
		if old.Phase == "applied" && phase == "undo_prepared" && next != nil && next.Started == 0 && len(next.Results) == 0 && len(next.Canceled) == 0 && next.Result == nil && undo != "" {
			return nil
		}
		return fmt.Errorf("invalid import inverse preparation")
	}
	if old.Phase != "undo_prepared" || next == nil || undo != old.UndoOperationID || !prior.At.Equal(next.At) || !reflect.DeepEqual(prior.Watches, next.Watches) || !reflect.DeepEqual(prior.Steps, next.Steps) || len(next.Results) < len(prior.Results) {
		return fmt.Errorf("import inverse rewrite changed its original plan or prior results")
	}
	// Native optional raw JSON can be omitted on GET and become null in the
	// archived record. Compare canonical bytes, retaining all evidence fields.
	before, err := json.Marshal(prior.Results)
	if err != nil {
		return err
	}
	after, err := json.Marshal(next.Results[:len(prior.Results)])
	if err != nil || !bytes.Equal(before, after) {
		return fmt.Errorf("import inverse rewrite changed prior native results")
	}
	if len(next.Canceled) < len(prior.Canceled) || !slices.Equal(next.Canceled[:len(prior.Canceled)], prior.Canceled) {
		return fmt.Errorf("import inverse rewrite changed prior pre-write cancellations")
	}
	if len(next.Canceled) != len(prior.Canceled) {
		if len(next.Canceled) == len(prior.Canceled)+1 && phase == "undo_prepared" && next.Result == nil && prior.Started == len(prior.Results)+1 && next.Started == len(prior.Results) && len(next.Results) == len(prior.Results) && next.Canceled[len(prior.Canceled)].Step == len(prior.Results) {
			return nil
		}
		return fmt.Errorf("invalid import inverse pre-write cancellation transition")
	}
	if phase == "undone" && next.Result != nil && prior.Started == len(prior.Steps) && len(prior.Results) == len(prior.Steps) && next.Started == prior.Started && len(next.Results) == len(prior.Results) {
		return nil
	}
	if phase == "undo_prepared" && next.Result == nil {
		if prior.Started == len(prior.Results) && next.Started == prior.Started+1 && len(next.Results) == len(prior.Results) {
			return nil
		}
		if prior.Started == len(prior.Results)+1 && next.Started == prior.Started && len(next.Results) == len(prior.Results)+1 {
			return nil
		}
	}
	return fmt.Errorf("invalid import inverse progress transition")
}

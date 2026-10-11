package ghstore

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
)

// Results are separate from the immutable original command envelope. A resumed
// execution is observed, never promoted to proof of earlier verified writes.
// Native results use the existing reference document transport, not root-sized
// copies of task bodies or histories.
type ActionImportForward struct {
	Observation CreationObservation `json:"observation"`
	Targets     []ImportNativeState `json:"targets"`
}

func (a ActionJournal) importForwardPlan() (*ImportPlan, []ImportItem, error) {
	items, err := a.ImportCommand.inputItems()
	if err != nil {
		return nil, nil, err
	}
	forward := a.ImportForward
	if forward == nil || len(forward.Targets) != len(items) {
		return nil, nil, fmt.Errorf("import result requires every original member")
	}
	snapshot := &Snapshot{fullHistory: true}
	identities := map[string]string{}
	for _, original := range a.ImportCommand.OriginalTargets {
		identities[original.IssueID] = original.OperationID
	}
	seen := map[string]bool{}
	for n, target := range forward.Targets {
		member := a.ImportCommand.Members[n]
		id := fmt.Sprintf("gh-%d", target.Issue.Number)
		if id == a.CarrierID || seen[id] || member.TargetID != "" && member.TargetID != id {
			return nil, nil, fmt.Errorf("import result changed original destination membership")
		}
		seen[id] = true
		if err := target.validate(a.ImportCommand.Repository, id); err != nil {
			return nil, nil, err
		}
		record, err := target.Issue.record()
		if err != nil {
			return nil, nil, err
		}
		record.repository = a.ImportCommand.Repository
		if original, exists := identities[id]; exists {
			if member.Action == "create" || original != record.meta.OperationID {
				return nil, nil, fmt.Errorf("import result replaced an original allocation identity")
			}
		} else if member.Action != "create" || record.meta.OperationID != member.OperationID {
			return nil, nil, fmt.Errorf("import allocation result differs from its source identity")
		}
		identities[id] = record.meta.OperationID
		snapshot.records = append(snapshot.records, *record)
		snapshot.entries = append(snapshot.entries, snapshotEntry{Issue: target.Issue, Comments: target.Comments})
	}
	index := make([]ImportTargetIdentity, 0, len(identities))
	for id, operation := range identities {
		index = append(index, ImportTargetIdentity{IssueID: id, OperationID: operation})
	}
	slices.SortFunc(index, func(a, b ImportTargetIdentity) int { return strings.Compare(a.IssueID, b.IssueID) })
	client := &Client{repo: a.ImportCommand.Repository}
	plan, err := client.planImportObservation(items, a.ImportCommand.Force, snapshot, index)
	return plan, items, err
}

func (a ActionJournal) validateImportForward() error {
	if a.Phase == "prepared" {
		if a.ImportForward != nil {
			return fmt.Errorf("prepared import cannot claim a completed result")
		}
		return nil
	}
	if a.ImportForward == nil {
		return fmt.Errorf("applied import requires its actual native result")
	}
	observation := a.ImportForward.Observation
	probe := ActionJournal{OperationID: a.OperationID, SessionID: observation.SessionID, IssueID: "gh-1", Action: "create", Phase: "prepared"}
	if err := probe.validate(); err != nil || observation.Actor == "" || observation.ObservedAt.IsZero() || observation.Proof != "verified" && observation.Proof != "observed" || observation.Proof == "verified" && observation.SessionID != a.SessionID {
		return fmt.Errorf("invalid import completion observation")
	}
	plan, items, err := a.importForwardPlan()
	if err != nil {
		return err
	}
	mapping := map[string]string{}
	for _, row := range plan.Rows {
		mapping[row.SourceKey] = row.TargetID
	}
	relations, err := importRelationshipProjection(items, mapping)
	if err != nil {
		return err
	}
	for n, row := range plan.Rows {
		member := a.ImportCommand.Members[n]
		target := a.ImportForward.Targets[n]
		beforeState, baseline := "open", []nativeStateEvent{}
		if member.Before != nil {
			beforeState, baseline = member.Before.Issue.State, member.Before.Events
		}
		// Reuse the native state-transition proof used by scalar updates. An
		// intervening close/reopen cycle is not justified by matching fields.
		eventProbe := ActionJournal{Before: &ActionIssueState{State: beforeState}, UpdateIntent: &ActionUpdateIntent{Target: ActionUpdateInput{State: target.Issue.State}, NativeEvents: &baseline}}
		if err := updateEventProof(eventProbe, target.Events); err != nil {
			return err
		}
		if row.Action != "skip" {
			return fmt.Errorf("import result contains an unfinished source: %s", row.SourceKey)
		}
		if member.Action == "skip" {
			before, err := importTargetBasis(member.Before.Issue)
			if err != nil || row.basis != before || !reflect.DeepEqual(member.Before.Comments, a.ImportForward.Targets[n].Comments) || !reflect.DeepEqual(member.Before.Events, a.ImportForward.Targets[n].Events) {
				return fmt.Errorf("import changed an originally skipped destination")
			}
			continue
		}
		if !row.HistoryVerified || row.SourceChanged || row.Owner == nil || row.Owner.OperationID != a.OperationID || row.Owner.SessionID != a.SessionID {
			return fmt.Errorf("import result lacks its owned completion and source provenance")
		}
		_, projected, err := importIssueProjection(items[n], a.ImportForward.Targets[n].Issue, row.Observed, relations[row.SourceKey])
		if err != nil {
			return err
		}
		basis, err := importTargetBasis(projected)
		if err != nil || basis != row.basis {
			return fmt.Errorf("import result differs from its frozen source projection")
		}
	}
	return nil
}

// This boundary has real root identity and native author/clocks. The logical
// validator above cannot infer those from caller-supplied JSON owner fields.
func validateImportForwardNative(a ActionJournal, native apiComment) error {
	if a.ImportForward == nil {
		return nil
	}
	if !strings.EqualFold(a.ImportForward.Observation.Actor, native.User.Login) {
		return fmt.Errorf("import completion author differs from its native root")
	}
	for _, target := range a.ImportForward.Targets {
		if target.Issue.UpdatedAt.After(native.UpdatedAt) {
			return fmt.Errorf("import completion predates its native target observation")
		}
		for _, checkpoint := range target.Comments {
			if checkpoint.UpdatedAt.After(native.UpdatedAt) {
				return fmt.Errorf("import completion predates its native comment observation")
			}
		}
	}
	plan, _, err := a.importForwardPlan()
	if err != nil {
		return err
	}
	root := &ActionJournalRecord{ActionJournal: a, ID: fmt.Sprintf("ghc-%d", native.ID), Author: native.User.Login, CreatedAt: native.CreatedAt, UpdatedAt: native.UpdatedAt}
	scope := &importExecutionScope{root: root, owner: ImportExecutionOwner{CommandID: root.ID, OperationID: root.OperationID, SessionID: root.SessionID}}
	return validateImportExecutionRows(scope, plan)
}

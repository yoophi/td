package ghstore

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
)

const actionImportCommandStart = actionJournalPrefix + "v33\n"

// Input is canonical portable JSON, retained verbatim instead of decoded with
// the legacy envelope normalizer. Members bind each source to the original
// destination decision. A create member has no invented destination identity.
type ActionImportCommand struct {
	Repository      string                 `json:"repository"`
	Input           string                 `json:"input"`
	Force           bool                   `json:"force"`
	OriginalTargets []ImportTargetIdentity `json:"original_targets"`
	Members         []ActionImportMember   `json:"members"`
	Compensations   []ImportCompensation   `json:"compensations,omitempty"`
}

type ActionImportMember struct {
	SourceKey   string             `json:"source_key"`
	Digest      string             `json:"digest"`
	OperationID string             `json:"operation_id"`
	Action      string             `json:"action"`
	TargetID    string             `json:"target_id,omitempty"`
	Before      *ImportNativeState `json:"before,omitempty"`
}

// Native observations are separate from portable source data. Keeping actual
// comments and state events lets later compensation distinguish retained source
// provenance from live activity and review authority in the destination.
type ImportNativeState struct {
	Issue    apiIssue           `json:"issue"`
	Comments []apiComment       `json:"comments"`
	Events   []nativeStateEvent `json:"events"`
}

func (i *ActionImportCommand) inputItems() ([]ImportItem, error) {
	type value ImportItem
	var values []value
	if err := decodeImportJSON([]byte(i.Input), &values); err != nil {
		return nil, fmt.Errorf("invalid frozen import input: %w", err)
	}
	canonical, err := json.Marshal(values)
	if err != nil || !bytes.Equal(canonical, []byte(i.Input)) || len(values) == 0 {
		return nil, fmt.Errorf("import command requires a nonempty canonical input array")
	}
	items := make([]ImportItem, len(values))
	for n := range values {
		items[n] = ImportItem(values[n])
	}
	if err := ValidateImportItems(items); err != nil {
		return nil, err
	}
	if err := RequireImportSourceNamespaces(items); err != nil {
		return nil, err
	}
	return items, nil
}

func (s *ImportNativeState) validate(repository, id string) error {
	if s == nil || fmt.Sprintf("gh-%d", s.Issue.Number) != id || s.Comments == nil || s.Events == nil {
		return fmt.Errorf("import destination requires its complete native observation")
	}
	r, err := s.Issue.record()
	if err != nil {
		return err
	}
	if r.meta.EntityKind != "" && r.meta.EntityKind != "issue" {
		return fmt.Errorf("import destination is not a task")
	}
	state, err := actionIssueState(r)
	if err != nil {
		return err
	}
	if err := state.validate(); err != nil {
		return err
	}
	client := &Client{repo: repository}
	last := int64(0)
	for _, comment := range s.Comments {
		number, err := client.commentIssueNumber(comment.IssueURL)
		if err != nil || number != s.Issue.Number || comment.ID <= last || comment.User.Login == "" || comment.CreatedAt.IsZero() || comment.UpdatedAt.Before(comment.CreatedAt) {
			return fmt.Errorf("import original comment has invalid native identity, order or clocks")
		}
		last = comment.ID
	}
	last = 0
	for _, event := range s.Events {
		if event.ID <= last || event.Event != "closed" && event.Event != "reopened" {
			return fmt.Errorf("invalid import original state event")
		}
		last = event.ID
	}
	return nil
}

func (a ActionJournal) validateImportCommand() error {
	i := a.ImportCommand
	if i == nil || !repositoryPattern.MatchString(i.Repository) || a.Action != "import" || !slices.Contains([]string{"prepared", "applied", "undo_prepared", "undone"}, a.Phase) || !canonicalGroupID(a.CarrierID) {
		return fmt.Errorf("import requires a repository, separate carrier and supported command phase")
	}
	// Import owns its own snapshots. It must not smuggle another command's
	// before/after state, child membership or unverified completion into v33.
	expected := ActionJournal{OperationID: a.OperationID, SessionID: a.SessionID, CarrierID: a.CarrierID, Action: "import", Phase: a.Phase, ImportCommand: i, ImportForward: a.ImportForward, ImportInverse: a.ImportInverse, UndoOperationID: a.UndoOperationID}
	if !reflect.DeepEqual(a, expected) {
		return fmt.Errorf("import intent contains unrelated action fields")
	}
	items, err := i.inputItems()
	if err != nil {
		return err
	}
	if i.OriginalTargets == nil || len(i.Members) != len(items) {
		return fmt.Errorf("import intent omitted original identities or source membership")
	}
	index, err := newImportTargetIndex(i.OriginalTargets)
	if err != nil {
		return err
	}
	if _, exists := index.byID[a.CarrierID]; exists {
		return fmt.Errorf("import carrier cannot be an original task")
	}
	seen := map[string]bool{}
	changes := false
	original := &Snapshot{fullHistory: true}
	for n, member := range i.Members {
		source := items[n]
		digest, err := source.Digest()
		if err != nil || member.SourceKey != source.SourceKey() || member.Digest != digest || member.OperationID != importOperationID(member.SourceKey) {
			return fmt.Errorf("import membership differs from its frozen source identity/input")
		}
		if !slices.Contains([]string{"create", "overwrite", "resume", "resume_history", "skip"}, member.Action) {
			return fmt.Errorf("unknown import destination decision")
		}
		changes = changes || member.Action != "skip"
		if member.Action == "create" {
			if member.TargetID != "" || member.Before != nil {
				return fmt.Errorf("unallocated import source cannot invent a destination observation")
			}
			continue
		}
		if _, exists := index.byID[member.TargetID]; !canonicalGroupID(member.TargetID) || !exists || seen[member.TargetID] {
			return fmt.Errorf("import destination is absent, repeated or not canonical")
		}
		seen[member.TargetID] = true
		if err := member.Before.validate(i.Repository, member.TargetID); err != nil {
			return err
		}
		record, err := member.Before.Issue.record()
		if err != nil {
			return err
		}
		record.repository = i.Repository
		original.records = append(original.records, *record)
		original.entries = append(original.entries, snapshotEntry{Issue: member.Before.Issue, Comments: member.Before.Comments})
	}
	if !changes {
		return fmt.Errorf("unchanged import must not publish a command")
	}
	client := &Client{repo: i.Repository}
	plan, err := client.planImportObservation(items, i.Force, original, i.OriginalTargets, i.Compensations)
	if err != nil {
		return fmt.Errorf("import original decisions cannot be reconstructed: %w", err)
	}
	var usedCompensations []ImportCompensation
	for n, row := range plan.Rows {
		member := i.Members[n]
		if row.Action != member.Action || row.TargetID != member.TargetID {
			return fmt.Errorf("import destination decision for %s differs from its original input/observation", row.SourceKey)
		}
		if row.compensation != nil {
			if stateEventDigest(member.Before.Events) != row.compensation.EventsSHA256 {
				return fmt.Errorf("import compensation native events changed before the next generation")
			}
			usedCompensations = append(usedCompensations, *row.compensation)
		}
	}
	if !reflect.DeepEqual(usedCompensations, i.Compensations) {
		return fmt.Errorf("import command contains unused or replaced compensation links")
	}
	if err := a.validateImportForward(); err != nil {
		return err
	}
	return a.validateImportInverse()
}

// Build from the original complete snapshot. No remote write occurs here, and
// no later replan may replace these source inputs or original observations.
func prepareImportCommand(items []ImportItem, force bool, plan *ImportPlan, events map[string][]nativeStateEvent) (*ActionImportCommand, error) {
	if plan == nil || plan.snapshot == nil || len(plan.Rows) != len(items) {
		return nil, fmt.Errorf("import command requires its original complete plan")
	}
	data, err := json.Marshal(items)
	if err != nil {
		return nil, err
	}
	input := &ActionImportCommand{Repository: plan.Repository, Input: string(data), Force: force, OriginalTargets: snapshotImportIdentities(plan.snapshot), Members: []ActionImportMember{}}
	entries := map[string]snapshotEntry{}
	for _, entry := range plan.snapshot.entries {
		entries[fmt.Sprintf("gh-%d", entry.Issue.Number)] = entry
	}
	for _, row := range plan.Rows {
		member := ActionImportMember{SourceKey: row.SourceKey, Digest: row.Digest, OperationID: row.OperationID, Action: row.Action, TargetID: row.TargetID}
		if row.TargetID != "" {
			entry, found := entries[row.TargetID]
			baseline, observed := events[row.TargetID]
			if !found || !observed || baseline == nil {
				return nil, fmt.Errorf("import destination lacks original native state/events: %s", row.TargetID)
			}
			basis, err := importTargetBasis(entry.Issue)
			if err != nil || basis != row.basis {
				return nil, fmt.Errorf("import destination differs from its original plan: %s", row.TargetID)
			}
			comments := append([]apiComment{}, entry.Comments...)
			slices.SortFunc(comments, func(a, b apiComment) int {
				if a.ID < b.ID {
					return -1
				}
				if a.ID > b.ID {
					return 1
				}
				return 0
			})
			member.Before = &ImportNativeState{Issue: entry.Issue, Comments: comments, Events: baseline}
		}
		input.Members = append(input.Members, member)
		if row.compensation != nil {
			input.Compensations = append(input.Compensations, *row.compensation)
		}
	}
	// Detach snapshots, event slices and nested native labels from the planner.
	data, err = json.Marshal(input)
	if err != nil {
		return nil, err
	}
	var frozen ActionImportCommand
	if err := json.Unmarshal(data, &frozen); err != nil {
		return nil, err
	}
	// The probe is only local validation, never persisted as native evidence.
	// Choose an unused identity rather than reserving a real repository number.
	carrier := 1
	for {
		if _, exists := entries[fmt.Sprintf("gh-%d", carrier)]; !exists {
			break
		}
		carrier++
	}
	probe := ActionJournal{OperationID: "import-probe", SessionID: "import-session", CarrierID: fmt.Sprintf("gh-%d", carrier), Action: "import", Phase: "prepared", ImportCommand: &frozen}
	if err := probe.validate(); err != nil {
		return nil, err
	}
	return &frozen, nil
}

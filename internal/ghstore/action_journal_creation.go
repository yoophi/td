package ghstore

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"
)

const actionCreationCommandStart = actionJournalPrefix + "v31\n"
const actionCreationRelationsStart = actionJournalPrefix + "v32\n"

// The initial intent contains requested edges and real external observations.
// Allocation and Relations are populated only from actual native results. The
// new task never acquires an invented before-state or a guessed GitHub number.
type ActionCreationCommand struct {
	DependsOn           []string                      `json:"depends_on"`
	Blocks              []string                      `json:"blocks"`
	OriginalIDs         []string                      `json:"original_ids"`
	Watches             []ActionRelatedObservation    `json:"watches"`
	Events              map[string][]nativeStateEvent `json:"events"`
	RelationOperationID string                        `json:"relation_operation_id"`
	Allocation          *CreationObservation          `json:"allocation,omitempty"`
	Relations           *ActionJournalRecord          `json:"relations,omitempty"`
	Forward             *CreationObservation          `json:"forward,omitempty"`
	Inverse             *ActionCreationInverse        `json:"inverse,omitempty"`
	Partial             bool                          `json:"partial,omitempty"`
}

type CreationObservation struct {
	Proof      string    `json:"proof"`
	SessionID  string    `json:"session_id"`
	Actor      string    `json:"actor"`
	ObservedAt time.Time `json:"observed_at"`
}

func (a ActionJournal) validateCreationScope() error {
	if a.CreationCommand != nil && (a.Action != "create" || a.CreateInput == nil || a.CarrierID == "" || a.Group != nil || a.GroupOperationID != "" || a.CreationOwner != "" || a.Reconciliation != nil) {
		return fmt.Errorf("creation command requires its own complete creation input and separate carrier")
	}
	if a.CreationOwner != "" {
		probe := ActionJournal{OperationID: a.CreationOwner, SessionID: a.SessionID, IssueID: "gh-1", Action: "create", Phase: "prepared"}
		if err := probe.validate(); err != nil || a.CreationOwner == a.OperationID || !a.Group.updateRelationsGroup() || a.CreationCommand != nil {
			return fmt.Errorf("creation child requires its distinct parent and a relationship command")
		}
	}
	return nil
}

func (a ActionJournal) validateCreationCommand() error {
	c := a.CreationCommand
	if c == nil {
		return nil
	}
	if c.DependsOn == nil || c.Blocks == nil || c.OriginalIDs == nil || c.Watches == nil || c.Events == nil {
		return fmt.Errorf("creation command requires complete original graph observations")
	}
	probe := ActionJournal{OperationID: c.RelationOperationID, SessionID: a.SessionID, IssueID: "gh-1", Action: "create", Phase: "prepared"}
	if err := probe.validate(); err != nil || c.RelationOperationID == a.OperationID {
		return fmt.Errorf("creation requires a distinct frozen relation operation")
	}
	ids := map[string]bool{}
	for _, id := range c.OriginalIDs {
		if !canonicalGroupID(id) || ids[id] || id == a.IssueID {
			return fmt.Errorf("creation allocation collides with original identities")
		}
		ids[id] = true
	}
	records, err := c.originalRecords("")
	if err != nil {
		return err
	}
	for id := range records {
		if !ids[id] {
			return fmt.Errorf("creation watch was absent from original membership")
		}
	}
	_, meta, _, err := decodeBody(a.CreateInput.Body)
	if err != nil {
		return err
	}
	if meta.EntityKind != "" && meta.EntityKind != "issue" {
		return fmt.Errorf("creation command requires task metadata")
	}
	parent := ""
	deps := []string{}
	if meta.Details != nil {
		parent, deps = meta.Details.ParentID, meta.Details.Dependencies
	}
	if !slices.Equal(deps, c.DependsOn) {
		return fmt.Errorf("creation POST dependencies differ from frozen request")
	}
	g, err := planCreationRelations(records, parent, RelationChanges{DependsOn: &c.DependsOn, Blocks: &c.Blocks})
	if err != nil {
		return err
	}
	if len(g.Watches) != len(c.Watches) {
		return fmt.Errorf("creation watch set differs from reachable original graph")
	}
	for _, id := range g.Watches {
		if _, found := records[id]; !found {
			return fmt.Errorf("creation watch missing: %s", id)
		}
	}
	for _, id := range c.Blocks {
		r := records[id]
		state, err := actionIssueState(&r)
		if err != nil {
			return err
		}
		events, found := c.Events[id]
		if updateIntentNeedsEvents(state, state) != found || found && events == nil {
			return fmt.Errorf("creation review event evidence missing or unrelated")
		}
		last := int64(0)
		for _, e := range events {
			if e.ID <= last || e.Event != "closed" && e.Event != "reopened" {
				return fmt.Errorf("invalid creation event evidence")
			}
			last = e.ID
		}
	}
	for id := range c.Events {
		if !slices.Contains(c.Blocks, id) {
			return fmt.Errorf("creation contains unrelated event evidence")
		}
	}
	if (a.After != nil) != (c.Allocation != nil) {
		return fmt.Errorf("creation allocation requires an actual result and proof")
	}
	for _, evidence := range []*CreationObservation{c.Allocation, c.Forward} {
		if evidence == nil {
			continue
		}
		if evidence.Proof != "verified" && evidence.Proof != "observed" || evidence.Actor == "" || evidence.ObservedAt.IsZero() {
			return fmt.Errorf("invalid creation result evidence")
		}
		probe.SessionID = evidence.SessionID
		if err := probe.validate(); err != nil {
			return err
		}
		if evidence.Proof == "verified" && evidence.SessionID != a.SessionID {
			return fmt.Errorf("verified creation result requires original session")
		}
	}
	if a.Phase == "prepared" {
		if c.Forward != nil || c.Relations != nil || c.Partial {
			return fmt.Errorf("prepared creation cannot claim a finished command")
		}
	} else {
		if c.Allocation == nil || c.Forward == nil {
			return fmt.Errorf("creation result requires allocation and whole-command evidence")
		}
		if c.Forward.Proof == "verified" && c.Allocation.Proof != "verified" {
			return fmt.Errorf("observed allocation cannot become a verified write")
		}
		if len(c.Blocks) != 0 && c.Relations == nil && !c.Partial {
			return fmt.Errorf("creation result lost its required relationship command")
		}
	}
	if c.Relations != nil {
		if err := validateCreationChild(&ActionJournalRecord{ActionJournal: a, Author: c.Allocation.Actor}, c.Relations); err != nil {
			return err
		}
		if c.Relations.Phase != "applied" || c.Relations.Group.Forward == nil {
			return fmt.Errorf("creation must freeze its original relation result")
		}
		if c.Partial != c.Relations.Group.Forward.Partial || c.Forward.Proof == "verified" && c.Relations.Group.Forward.Proof != "verified-group-receipts" {
			return fmt.Errorf("creation relation outcome lost partial or observed provenance")
		}
	}
	return a.validateCreationInverse()
}

func (c *ActionCreationCommand) originalRecords(repo string) (map[string]Record, error) {
	all := map[string]Record{}
	for _, watch := range c.Watches {
		if _, found := all[watch.ID]; found || !canonicalGroupID(watch.ID) {
			return nil, fmt.Errorf("creation watches have duplicate or invalid identity")
		}
		if err := watch.State.validate(); err != nil {
			return nil, err
		}
		r, err := actionStateRecord(&watch.State, watch.ID)
		if err != nil {
			return nil, err
		}
		if r.DeletedAt != nil || r.meta.EntityKind != "" && r.meta.EntityKind != "issue" {
			return nil, fmt.Errorf("creation watches require live tasks")
		}
		r.repository = repo
		all[r.ID] = *r
	}
	return all, nil
}

func creationNativeAuthor(a ActionJournal, author string) bool {
	c := a.CreationCommand
	return c == nil || (c.Allocation == nil || strings.EqualFold(c.Allocation.Actor, author)) && (c.Forward == nil || strings.EqualFold(c.Forward.Actor, author)) && (c.Relations == nil || strings.EqualFold(c.Relations.Author, author)) && (c.Inverse == nil || c.Inverse.Result == nil || strings.EqualFold(c.Inverse.Result.Actor, author))
}

// A child owns only reverse-edge edits; the new task's initial dependencies
// were already written by POST. Every external before-state remains original.
func validateCreationChild(root, child *ActionJournalRecord) error {
	c := root.CreationCommand
	if c == nil || root.After == nil || child == nil || child.CreationOwner != root.OperationID || child.OperationID != c.RelationOperationID || child.SessionID != root.SessionID || child.CarrierID != root.CarrierID || child.IssueID != root.IssueID || !strings.EqualFold(child.Author, root.Author) || !child.Group.updateRelationsGroup() {
		return fmt.Errorf("creation child differs from its frozen parent scope")
	}
	if _, err := CommentNumber(child.ID); err != nil || child.CreatedAt.IsZero() || child.UpdatedAt.Before(child.CreatedAt) {
		return fmt.Errorf("creation child native identity or clocks invalid")
	}
	if err := child.validate(); err != nil {
		return err
	}
	g := child.Group
	if !slices.Equal(g.RelationUpdates.Selected, []string{root.IssueID}) || len(g.Members) != len(c.Blocks)+1 {
		return fmt.Errorf("creation child changed selected membership")
	}
	original, err := c.originalRecords("")
	if err != nil {
		return err
	}
	for i, member := range g.Members {
		input := g.RelationUpdates.Inputs[i]
		if input.Comment != nil {
			return fmt.Errorf("creation child cannot add undeclared activity")
		}
		if member.IssueID == root.IssueID {
			if !reflect.DeepEqual(member.Before, *root.After) || input.Update != nil {
				return fmt.Errorf("creation child replaced allocated state")
			}
			continue
		}
		if !slices.Contains(c.Blocks, member.IssueID) || input.Update == nil {
			return fmt.Errorf("creation child has undeclared effect")
		}
		before := original[member.IssueID]
		state, err := actionIssueState(&before)
		if err != nil || !reflect.DeepEqual(member.Before, state) {
			return fmt.Errorf("creation child replaced original external observation")
		}
		target, err := input.Update.Target.contents()
		if err != nil {
			return err
		}
		// Private semantic validation only; no projected observation is persisted.
		target.UpdatedAt = state.UpdatedAt
		target.Revision, _ = target.checksum()
		after, err := actionStateRecord(&target, member.IssueID)
		if err != nil {
			return err
		}
		bd, err := before.CopyDetails()
		if err != nil {
			return err
		}
		ad, err := after.CopyDetails()
		if err != nil {
			return err
		}
		labels := mirroredPriorityLabels(mirroredLabels(state.Labels, before.Status), before.Priority)
		if !slices.Equal(ad.Dependencies, append(slices.Clone(bd.Dependencies), root.IssueID)) || bd.ParentID != ad.ParentID || before.Title != after.Title || before.Description != after.Description || before.Type != after.Type || before.Priority != after.Priority || before.Points != after.Points || before.Acceptance != after.Acceptance || !sameLabels(labels, target.Labels) || bd.Minor != ad.Minor || bd.Sprint != ad.Sprint || !reflect.DeepEqual(bd.Files, ad.Files) || !reflect.DeepEqual(bd.DueDate, ad.DueDate) || !reflect.DeepEqual(bd.DeferUntil, ad.DeferUntil) || bd.DeferCount != ad.DeferCount {
			return fmt.Errorf("creation child changed fields outside its requested reverse edge")
		}
		probe := &ActionJournalRecord{ActionJournal: ActionJournal{Action: "update", SessionID: root.SessionID, IssueID: member.IssueID, Before: &state, After: &target}}
		if ok, err := relationUndoScope(probe); err != nil || !ok {
			return fmt.Errorf("creation child has an invalid relationship delta")
		}
		if events, found := c.Events[member.IssueID]; found && (input.Update.NativeEvents == nil || !reflect.DeepEqual(events, *input.Update.NativeEvents)) {
			return fmt.Errorf("creation child replaced original event evidence")
		}
	}
	return nil
}

func checkCreationCommandCapacity(a ActionJournal) error {
	if _, err := planActionJournalPublication(a); err != nil {
		return err
	}
	data, err := json.Marshal(a)
	if err != nil {
		return err
	}
	// Each reverse target may refer to the complete original graph. Reserve
	// space for those copies, allocation, inverse evidence and deletion input
	// before POST; the bound is deliberately conservative, not a future state.
	copies := 8 * (len(a.CreationCommand.Blocks) + 1)
	if len(data) > actionDecodedBudget/copies {
		return fmt.Errorf("creation command exceeds reserved journal capacity before issue POST")
	}
	return nil
}

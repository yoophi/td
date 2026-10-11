package ghstore

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/marcus/td/internal/models"
)

type creationCommandContextKey struct{}
type activeCreationCommand struct {
	plan      *creationCommandPlan
	journal   *ActionJournalRecord
	allocated *Record
}

func creationContext(ctx context.Context) *activeCreationCommand {
	active, _ := ctx.Value(creationCommandContextKey{}).(*activeCreationCommand)
	return active
}

// CreateWithRelations freezes the entire request before creation. An error may
// accompany an allocated record; callers must preserve that ID and must not
// silently retry creation. Receipt recovery never replays the original POST.
func (c *Client) CreateWithRelations(ctx context.Context, issue *models.Issue, relations RelationChanges) (*Record, error) {
	if c.journal == nil || c.journal.Group != nil || creationContext(ctx) != nil {
		return nil, fmt.Errorf("related creation requires a tracked, independent command")
	}
	plan, err := c.prepareRelatedCreation(ctx, issue, relations, "td-op-"+rand.Text())
	if err != nil {
		return nil, err
	}
	// Capacity checks use a private carrier placeholder, never a guessed task.
	probe := ActionJournal{OperationID: plan.create.operationID, SessionID: c.journal.SessionID, CarrierID: "gh-2147483647", Action: "create", Phase: "prepared", CreateInput: &plan.create.input, CreationCommand: creationCommandFromPlan(plan)}
	if err := checkCreationCommandCapacity(probe); err != nil {
		return nil, err
	}
	active := &activeCreationCommand{plan: plan}
	ctx = context.WithValue(ctx, creationCommandContextKey{}, active)
	created, err := c.writeIssueCreation(ctx, plan.create)
	if err != nil {
		return active.allocated, creationCommandError(active, err)
	}
	if active.journal == nil || active.journal.After == nil {
		return created, fmt.Errorf("creation result was not linked to its command intent; inspect %s before retrying", created.ID)
	}
	var child *ActionJournalRecord
	if len(plan.graph.Blocks) != 0 {
		command, err := c.prepareCreatedRelations(ctx, plan, created)
		if err != nil {
			return created, creationCommandError(active, err)
		}
		command.root.CreationOwner = active.journal.OperationID
		if _, err := c.writeRelationCommand(ctx, command); err != nil {
			return created, creationCommandError(active, err)
		}
		child = command.receipt
		if err := validateCreationChild(active.journal, child); err != nil {
			return created, creationCommandError(active, err)
		}
	}
	next, err := copyCreationJournal(active.journal.ActionJournal)
	if err != nil {
		return created, err
	}
	next.Phase = "applied"
	next.CreationCommand.Relations = child
	next.CreationCommand.Forward = &CreationObservation{Proof: "verified", SessionID: next.SessionID, Actor: active.journal.Author, ObservedAt: time.Now().UTC()}
	fence := func() error {
		if child != nil {
			_, err := c.observePairedGroup(ctx, child, true)
			return err
		}
		return c.verifyCreationOriginal(ctx, active.journal, created)
	}
	if _, err := c.writeCreationReceipt(ctx, active.journal, next, fence); err != nil {
		return created, creationCommandError(active, err)
	}
	return created, nil
}

func creationCommandFromPlan(p *creationCommandPlan) *ActionCreationCommand {
	ids := []string{}
	for id := range p.records {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return &ActionCreationCommand{DependsOn: slices.Clone(p.graph.DependsOn), Blocks: slices.Clone(p.graph.Blocks), OriginalIDs: ids, Watches: p.watches, Events: p.events, RelationOperationID: p.relationOperationID}
}

func creationCommandError(active *activeCreationCommand, err error) error {
	id, task := "unpublished", "unallocated"
	if active.journal != nil {
		id = active.journal.ID
	}
	if active.allocated != nil {
		task = active.allocated.ID
		return fmt.Errorf("issue %s was created; creation command %s (operation %s) is incomplete; earlier changes remain; inspect and reconcile without repeating creation: %w", task, id, active.plan.create.operationID, err)
	}
	return fmt.Errorf("creation command %s (task %s, operation %s) is incomplete; earlier changes remain; inspect and reconcile without repeating creation: %w", id, task, active.plan.create.operationID, err)
}

func copyCreationJournal(a ActionJournal) (ActionJournal, error) {
	data, err := json.Marshal(a)
	if err != nil {
		return ActionJournal{}, err
	}
	var out ActionJournal
	err = json.Unmarshal(data, &out)
	return out, err
}

func (c *Client) verifyCreationReceipt(ctx context.Context, a *ActionJournalRecord) error {
	if a == nil {
		return fmt.Errorf("creation has no durable parent intent")
	}
	body, err := renderExistingActionJournal(a)
	if err != nil {
		return err
	}
	n, err := CommentNumber(a.ID)
	if err != nil {
		return err
	}
	current, err := c.journalComment(ctx, fmt.Sprintf("/issues/comments/%d", n), nil, body, a.CarrierID)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(current, a) {
		return fmt.Errorf("creation parent intent changed; no further target write attempted")
	}
	return nil
}

// This is the original pre-POST boundary, also used when no reverse effects
// exist. Relationship execution subsequently uses its own complete graph fence.
func (c *Client) verifyCreationOriginal(ctx context.Context, a *ActionJournalRecord, created *Record) error {
	if err := c.verifyCreationReceipt(ctx, a); err != nil {
		return err
	}
	snapshot, err := c.ReadSnapshot(WithFreshSnapshot(ctx), false)
	if err != nil {
		return err
	}
	records := map[string]Record{}
	allocations := 0
	for _, r := range snapshot.Issues(true) {
		records[r.ID] = r
		if r.meta.OperationID == a.OperationID {
			allocations++
			if created == nil || r.ID != created.ID || r.revision != created.revision {
				return fmt.Errorf("creation allocation is unexpected or changed")
			}
		}
	}
	if created != nil && allocations != 1 {
		return fmt.Errorf("creation requires exactly one observed allocation")
	}
	for _, watch := range a.CreationCommand.Watches {
		r, found := records[watch.ID]
		if !found {
			return &ConflictError{ID: watch.ID}
		}
		state, err := actionIssueState(&r)
		if err != nil {
			return err
		}
		if state.Revision != watch.State.Revision {
			return &ConflictError{ID: watch.ID}
		}
	}
	for id, original := range a.CreationCommand.Events {
		events, err := c.readStateEvents(ctx, id)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(events, original) {
			return &ConflictError{ID: id}
		}
	}
	return nil
}

func (c *Client) writeCreationReceipt(ctx context.Context, old *ActionJournalRecord, next ActionJournal, fence func() error) (*ActionJournalRecord, error) {
	if old.CreationCommand == nil || next.CreationCommand == nil {
		return nil, fmt.Errorf("creation receipt requires its original command")
	}
	before, err := copyCreationJournal(old.ActionJournal)
	if err != nil {
		return nil, err
	}
	baseline, err := copyCreationJournal(next)
	if err != nil {
		return nil, err
	}
	// Forward completion preserves the already verified allocation and every
	// original input. Recovery/undo require their own explicit phase contracts.
	if old.Phase != "prepared" || next.Phase != "applied" {
		return nil, fmt.Errorf("invalid creation forward completion")
	}
	if old.After == nil {
		if next.After == nil || next.CreationCommand.Allocation == nil || next.CreationCommand.Allocation.Proof != "observed" || next.CreationCommand.Forward == nil || next.CreationCommand.Forward.Proof != "observed" {
			return nil, fmt.Errorf("uncertain allocation can only be linked as an observed result")
		}
		baseline.IssueID, baseline.After, baseline.CreationCommand.Allocation = before.IssueID, before.After, before.CreationCommand.Allocation
	}
	baseline.Phase = before.Phase
	baseline.CreationCommand.Relations, baseline.CreationCommand.Forward, baseline.CreationCommand.Partial = nil, nil, false
	if !reflect.DeepEqual(before, baseline) {
		return nil, fmt.Errorf("creation completion changed original input or allocation")
	}
	publication, err := planActionJournalPublication(next)
	if err != nil {
		return nil, err
	}
	if err := c.verifyCreationReceipt(ctx, old); err != nil {
		return nil, err
	}
	if fence != nil {
		if err := fence(); err != nil {
			return nil, err
		}
	}
	n, _ := CommentNumber(old.ID)
	return c.publishJournal(ctx, fmt.Sprintf("/issues/comments/%d", n), publication, old, fence)
}

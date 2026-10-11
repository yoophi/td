package ghstore

import (
	"context"
	"fmt"
	"reflect"
	"slices"
)

// prepareCreatedRelations binds the actual creation result to the pre-POST
// observations. It only prepares a child command; publishing a durable parent
// intent and proving the creation response/author remain the caller's duties.
// In particular this helper is not a public create-with-relations entrypoint.
func (c *Client) prepareCreatedRelations(ctx context.Context, original *creationCommandPlan, created *Record) (*relationCommandPlan, error) {
	if c.journal == nil || c.journal.Group != nil || original == nil || original.create == nil || original.graph == nil {
		return nil, fmt.Errorf("creation relation binding requires its tracked original plan")
	}
	records, err := c.bindCreatedRecord(original, created)
	if err != nil {
		return nil, err
	}
	blocks := slices.Clone(original.graph.Blocks)
	command, err := c.prepareRelationCommand(ctx, records, []string{created.ID}, Changes{}, RelationChanges{Blocks: &blocks}, nil)
	if err != nil {
		return nil, err
	}
	command.root.OperationID = original.relationOperationID
	g := command.root.Group
	for i, m := range g.Members {
		if events, required := original.events[m.IssueID]; required {
			input := g.RelationUpdates.Inputs[i].Update
			if input == nil || input.NativeEvents == nil || !reflect.DeepEqual(*input.NativeEvents, events) {
				return nil, fmt.Errorf("creation relationship event baseline changed for %s: %w", m.IssueID, &ConflictError{ID: m.IssueID})
			}
		}
	}
	// The child's ordinary graph watches do not include an unchanged parent of
	// the new task. Retain the parent's complete original ancestry, too.
	for _, watch := range original.watches {
		if member, exists := command.expected[watch.ID]; exists {
			if member.Revision != watch.State.Revision {
				return nil, &ConflictError{ID: watch.ID}
			}
			continue
		}
		found := false
		for _, current := range g.RelationUpdates.Watches {
			if current.ID == watch.ID {
				if current.State.Revision != watch.State.Revision {
					return nil, &ConflictError{ID: watch.ID}
				}
				found = true
			}
		}
		if !found {
			state := watch.State
			state.Labels = slices.Clone(state.Labels)
			g.RelationUpdates.Watches = append(g.RelationUpdates.Watches, ActionRelatedObservation{ID: watch.ID, State: state})
		}
	}
	slices.SortFunc(g.RelationUpdates.Watches, func(a, b ActionRelatedObservation) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	// Adding original watches changes archive size; check before any child
	// intent, label or target write. Sizing uses the prepared full inputs.
	sizing := map[string]ActionIssueState{}
	for i, member := range g.Members {
		state := member.Before
		if input := g.RelationUpdates.Inputs[i].Update; input != nil {
			state, err = input.Target.contents()
			if err != nil {
				return nil, err
			}
			state.UpdatedAt = member.Before.UpdatedAt
			state.Revision, err = state.checksum()
			if err != nil {
				return nil, err
			}
		}
		sizing[member.IssueID] = state
	}
	if err := checkRelationCommandCapacity(command.root, sizing); err != nil {
		return nil, err
	}
	return command, nil
}

func (c *Client) bindCreatedRecord(original *creationCommandPlan, created *Record) (map[string]Record, error) {
	if created == nil || created.repository != c.repo || created.revision == ([32]byte{}) {
		return nil, fmt.Errorf("creation binding requires an actual result from this repository")
	}
	if _, err := original.graph.bindCreatedID(created.ID, original.records); err != nil {
		return nil, err
	}
	input := original.create.input
	if n, err := Number(created.ID); err != nil || n != created.Number || created.meta.OperationID != original.create.operationID || created.Title != input.Title || created.nativeBody != input.Body || created.nativeState != "open" {
		return nil, fmt.Errorf("allocated creation result differs from its frozen input or operation")
	}
	if err := checkLabels(created, input.Labels, "created"); err != nil {
		return nil, err
	}
	// Round-trip actual native states to detach every caller-owned detail/label
	// pointer. No projected timestamp or future revision becomes an observation.
	records := map[string]Record{}
	clone := func(record Record) error {
		state, err := actionIssueState(&record)
		if err != nil {
			return err
		}
		owned, err := actionStateRecord(&state, record.ID)
		if err != nil {
			return err
		}
		if owned.revision != record.revision {
			return fmt.Errorf("creation observation's native contents changed for %s", record.ID)
		}
		owned.repository, owned.URL = c.repo, record.URL
		records[record.ID] = *owned
		return nil
	}
	for _, record := range original.records {
		if err := clone(record); err != nil {
			return nil, err
		}
	}
	if err := clone(*created); err != nil {
		return nil, err
	}
	return records, nil
}

package ghstore

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// ApplyBuiltinBoardTransferObserved preserves an existing builtin configuration
// and appends conflict-free positions in one journaled PATCH. A virtual builtin
// is materialized with all initial settings in one journaled creation. This is
// a checked write, not reconciliation or an atomic GitHub transaction.
func (c *Client) ApplyBuiltinBoardTransferObserved(ctx context.Context, observed *BoardRecord, actor string, options BoardCreateOptions) (*BoardRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(actor) == "" {
		return nil, &WorkflowInputError{Reason: "builtin transfer requires an actual actor"}
	}
	operation, err := c.entityCreationOperation(options.OperationID, "td-board-op-", actor)
	if err != nil {
		return nil, err
	}
	if observed == nil || observed.ID != "bd-all-issues" || !observed.IsBuiltin || observed.observed.repository != c.repo || observed.Number != observed.observed.Number {
		return nil, &WorkflowInputError{Reason: "builtin transfer requires its observation from this repository"}
	}
	mode := options.ViewMode
	if mode == "" {
		mode = "swimlanes"
	}
	positions := slices.Clone(options.Positions)
	probe := BoardDetails{Version: 1, Builtin: true, ViewMode: mode, Positions: positions}
	if err := probe.validate(); err != nil {
		return nil, &WorkflowInputError{Reason: err.Error()}
	}
	if observed.Number == 0 {
		if observed.observed.revision != ([32]byte{}) {
			return nil, &WorkflowInputError{Reason: "invalid virtual builtin observation"}
		}
		if err := c.validateBoardPositionTargets(ctx, positions); err != nil {
			return nil, err
		}
		return c.createBoardConfigured(ctx, "All Issues", "", actor, true, &boardConfigMutation{reason: "Transferred initial builtin settings", apply: func(d *BoardDetails) error { d.ViewMode = mode; d.Positions = slices.Clone(positions); return nil }}, operation)
	}
	before, err := boardRecord(&observed.observed)
	if err != nil || !before.IsBuiltin || before.Details.DeletedAt != nil {
		return nil, &WorkflowInputError{Reason: "builtin transfer observation is not an active builtin"}
	}
	if options.OperationID != "" {
		for _, event := range before.Details.History {
			if event.OperationID == options.OperationID {
				return nil, fmt.Errorf("builtin operation %s already appears in target history; inspect its journal before reconciliation; no target PATCH attempted", options.OperationID)
			}
		}
	}
	if len(positions) == 0 {
		fresh, err := c.boardCarrier(ctx, before.Number)
		if err != nil {
			return nil, err
		}
		if fresh.observed.revision != before.observed.revision {
			return nil, &ConflictError{ID: before.ID}
		}
		return fresh, nil
	}
	merged := slices.Clone(before.Details.Positions)
	merged = append(merged, positions...)
	probe = before.Details
	probe.Positions = merged
	if err := probe.validate(); err != nil {
		return nil, fmt.Errorf("builtin transfer positions conflict; no write attempted: %w", err)
	}
	if err := c.validateBoardPositionTargets(ctx, positions); err != nil {
		return nil, err
	}
	return c.writeBoardObserved(ctx, observed, BoardChanges{}, false, actor, "Transferred builtin positions", &boardConfigMutation{operation: options.OperationID, action: "set_position", apply: func(d *BoardDetails) error { d.Positions = slices.Clone(merged); return nil }})
}

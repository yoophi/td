package ghstore

import (
	"context"
	"fmt"
	"reflect"
)

// Auxiliary inverses restore one native title/body and retain the immutable
// original action in the separate journal carrier. They do not alter tasks.
func entityUndoScope(action *ActionJournalRecord) error {
	kind := action.entityKind()
	if action.Action != kind || action.Before == nil || action.After == nil {
		return &UndoScopeError{Reason: "auxiliary creation cannot be erased safely; no target write attempted"}
	}
	if err := action.validateEntityAction(); err != nil {
		return err
	}
	_, before, _, err := decodeBody(action.Before.Body)
	if err != nil {
		return err
	}
	_, after, _, err := decodeBody(action.After.Body)
	if err != nil {
		return err
	}
	if kind == "board" {
		if before.Board == nil || after.Board == nil || before.Board.Builtin != after.Board.Builtin || (before.Board.Builtin && action.Before.Title != action.After.Title) {
			return &UndoScopeError{Reason: "board inverse cannot change builtin identity; no target write attempted"}
		}
	}
	// Metadata outside the entity configuration must remain identical.
	before.Board, after.Board = nil, nil
	before.Note, after.Note = nil, nil
	before.OperationID, after.OperationID = "", ""
	if !reflect.DeepEqual(before, after) {
		return &UndoScopeError{Reason: "auxiliary inverse cannot change unrelated metadata; no target write attempted"}
	}
	return nil
}

// Recheck the active catalog before intent, immediately before target PATCH,
// after readback and after receipt. GitHub offers no atomic catalog transaction.
func (c *Client) checkEntityUndoCatalog(ctx context.Context, action *ActionJournalRecord, afterWrite bool) error {
	if action.entityKind() != "board" {
		return nil
	}
	_, meta, _, err := decodeBody(action.Before.Body)
	if err != nil {
		return err
	}
	if meta.Board.DeletedAt != nil {
		return nil
	}
	_, id := action.EntityIdentity()
	if err := c.checkBoardName(ctx, action.Before.Title, id); err != nil {
		return fmt.Errorf("board undo catalog conflict (%v): %w", err, &ConflictError{ID: action.IssueID, AfterWrite: afterWrite})
	}
	return nil
}

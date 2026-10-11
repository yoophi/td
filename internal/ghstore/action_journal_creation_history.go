package ghstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
)

type creationChildContextKey struct{}

func checkCreationChildAccess(ctx context.Context, a *ActionJournalRecord) error {
	if a.CreationOwner == "" {
		return nil
	}
	owner, _ := ctx.Value(creationChildContextKey{}).(string)
	if owner != a.CreationOwner {
		return &UndoScopeError{Reason: "creation relationship child must be recovered or undone through its complete creation command; no target write attempted"}
	}
	return nil
}

func sameCreationChildReceipt(a, b *ActionJournalRecord) bool {
	x, err := json.Marshal(a)
	if err != nil {
		return false
	}
	y, err := json.Marshal(b)
	return err == nil && bytes.Equal(x, y)
}

// Ordinary group members have already been validated/collapsed. Keep creation
// at the newest position occupied by either its root or owned relationship
// group without rewriting any actual native identity or timestamp.
func collapseCreationCommands(actions []ActionJournalRecord) ([]ActionJournalRecord, error) {
	roots := map[string]*ActionJournalRecord{}
	children := map[string]*ActionJournalRecord{}
	for i := range actions {
		a := &actions[i]
		if a.CreationCommand != nil {
			if roots[a.OperationID] != nil {
				return nil, fmt.Errorf("duplicate creation command")
			}
			roots[a.OperationID] = a
		}
		if a.CreationOwner != "" {
			if children[a.CreationOwner] != nil {
				return nil, fmt.Errorf("creation has multiple relationship children")
			}
			children[a.CreationOwner] = a
		}
	}
	for owner, child := range children {
		root := roots[owner]
		if root == nil {
			return nil, fmt.Errorf("creation child %s has no observed parent intent", child.ID)
		}
		if err := validateCreationChild(root, child); err != nil {
			return nil, err
		}
		if err := validateCreationChildProgress(root, child); err != nil {
			return nil, err
		}
	}
	for op, root := range roots {
		if root.CreationCommand.Relations != nil && children[op] == nil {
			return nil, fmt.Errorf("creation command lost its original relationship receipt")
		}
	}
	out, seen := []ActionJournalRecord{}, map[string]bool{}
	for _, a := range actions {
		if a.CreationOwner != "" {
			a = *roots[a.CreationOwner]
		}
		if !seen[a.OperationID] {
			out = append(out, a)
			seen[a.OperationID] = true
		}
	}
	return out, nil
}

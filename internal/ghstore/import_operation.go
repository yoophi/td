package ghstore

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type importOperationKey struct{}

// ImportWithOperationID pins one tracked whole-command identity across explicit
// workspace attempts. It never derives that identity from source actors/history.
func (c *Client) ImportWithOperationID(ctx context.Context, items []ImportItem, force bool, operation string) (*ImportResult, error) {
	if c == nil || c.journal == nil || c.journal.Group != nil || operation == "" {
		return nil, fmt.Errorf("pinned import requires an independent tracked actor and operation; no API write attempted")
	}
	if _, err := c.entityCreationOperation(operation, "", c.journal.SessionID); err != nil {
		return nil, fmt.Errorf("invalid pinned import operation: %w", err)
	}
	return c.Import(context.WithValue(ctx, importOperationKey{}, operation), items, force)
}

// Search already validated bulk history; no per-source root query is needed.
func findPinnedImportCommand(snapshot *Snapshot, items []ImportItem, force bool, operation, session, actor string) (*ActionJournalRecord, error) {
	input, err := json.Marshal(items)
	if err != nil {
		return nil, err
	}
	history, err := snapshot.ActionHistory("")
	if err != nil {
		return nil, err
	}
	var found *ActionJournalRecord
	for i := range history {
		root := &history[i]
		if root.OperationID != operation {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("pinned import operation has duplicate native roots; inspect before resuming")
		}
		if root.ImportCommand == nil || root.ImportCommand.Input != string(input) || root.ImportCommand.Force != force || root.SessionID != session || !strings.EqualFold(root.Author, actor) {
			return nil, fmt.Errorf("pinned import operation differs from original input/force/actor; no write attempted")
		}
		if root.Phase != "prepared" && root.Phase != "applied" {
			return nil, fmt.Errorf("pinned import operation is %s; inspect original inverse before another workspace execution", root.Phase)
		}
		found = root
	}
	return found, nil
}

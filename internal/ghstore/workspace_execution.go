package ghstore

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// PublishWorkspaceArchiveData keeps carrier selection inside the journal module.
// Capacity and syntax are checked before journal bootstrap can write anything.
func (c *Client) PublishWorkspaceArchiveData(ctx context.Context, operation string, source []byte, mappings map[string]string, maxBytes int64) (*WorkspaceArchiveEvidence, error) {
	if c == nil || c.journal == nil {
		return nil, fmt.Errorf("workspace publication requires the actual tracked actor")
	}
	if _, err := PlanWorkspaceArchive(c.repo, "gh-"+strconv.Itoa(int(^uint(0)>>1)), operation, source, mappings, maxBytes); err != nil {
		return nil, err
	}
	if err := c.EnableActionJournal(ctx, c.journal.SessionID); err != nil {
		return nil, err
	}
	plan, err := PlanWorkspaceArchive(c.repo, c.journal.Carrier.ID, operation, source, mappings, maxBytes)
	if err != nil {
		return nil, err
	}
	return c.PublishWorkspaceArchive(ctx, plan)
}

// VerifiedEntityOperationMappings uses one fresh bulk issue/comment observation
// for every requested operation. A created/updated entity must still exactly
// match its native applied receipt, including the server clock. Observed-only,
// undone, foreign-session and incomplete actions do not become completion proof.
func (c *Client) VerifiedEntityOperationMappings(ctx context.Context, operations map[string]string) (map[string]string, error) {
	if c == nil || c.journal == nil {
		return nil, fmt.Errorf("entity verification requires the actual tracked actor")
	}
	for op, kind := range operations {
		if op == "" || kind != "note" && kind != "board" {
			return nil, fmt.Errorf("invalid entity operation request")
		}
	}
	if len(operations) == 0 {
		return map[string]string{}, nil
	}
	profile, err := c.requestAPI(ctx, "GET", "user", "/user", nil, false)
	if err != nil {
		return nil, err
	}
	var actor struct{ Login string }
	if err := json.Unmarshal(profile, &actor); err != nil {
		return nil, err
	}
	if actor.Login == "" {
		return nil, fmt.Errorf("entity verification requires the actual GitHub author")
	}
	snapshot, err := c.ReadSnapshot(WithFreshSnapshot(ctx), true)
	if err != nil {
		return nil, err
	}
	history, err := snapshot.ActionHistory("")
	if err != nil {
		return nil, err
	}
	records := map[string]*Record{}
	for i := range snapshot.records {
		records[snapshot.records[i].ID] = &snapshot.records[i]
	}
	result := map[string]string{}
	used := map[string]bool{}
	for _, action := range history {
		kind, wanted := operations[action.OperationID]
		if !wanted {
			continue
		}
		supportedAction := action.Action == "create" || kind == "board" && action.Action == "board"
		if action.Phase != "applied" || action.SessionID != c.journal.SessionID || !strings.EqualFold(action.Author, actor.Login) || action.After == nil || action.Restored != nil || action.UpdateObservation != nil || action.UndoObservation != nil || action.CreationCommand != nil || !supportedAction {
			return nil, fmt.Errorf("entity operation %s lacks an applied receipt for its original tracked actor", action.OperationID)
		}
		record := records[action.IssueID]
		if record == nil || record.meta.EntityKind != kind || record.meta.OperationID != action.OperationID {
			return nil, fmt.Errorf("entity operation %s target identity/kind changed", action.OperationID)
		}
		if action.Action == "create" {
			for _, entry := range snapshot.entries {
				if entry.Issue.Number == record.Number && !strings.EqualFold(entry.Issue.User.Login, action.Author) {
					return nil, fmt.Errorf("entity creator differs from its native creation receipt")
				}
			}
		}
		state, err := actionIssueState(record)
		if err != nil {
			return nil, err
		}
		if state.Revision != action.After.Revision {
			return nil, &ConflictError{ID: record.ID}
		}
		id := ""
		if kind == "note" {
			note, err := noteRecord(record)
			if err != nil {
				return nil, err
			}
			id = note.ID
		} else {
			board, err := boardRecord(record)
			if err != nil {
				return nil, err
			}
			id = board.ID
		}
		if used[id] {
			return nil, fmt.Errorf("entity completion maps multiple operations to %s", id)
		}
		used[id] = true
		result[action.OperationID] = id
	}
	if len(result) != len(operations) {
		return nil, fmt.Errorf("entity operation mapping is incomplete; inspect native receipts before workspace completion")
	}
	return result, nil
}

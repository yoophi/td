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

// ResumeWorkspaceArchiveData revalidates the existing carrier and original
// upload. It never bootstraps a missing carrier from an uncertain observation.
func (c *Client) ResumeWorkspaceArchiveData(ctx context.Context, operation string, source []byte, mappings map[string]string, maxBytes int64) (*WorkspaceArchiveEvidence, error) {
	if recorder, _ := ctx.Value(mutationRecorderKey{}).(MutationResumeRecorder); recorder == nil || !recorder.ReplayGuardEnabled() {
		return nil, fmt.Errorf("workspace resume requires the original durable replay guard; no write attempted")
	}
	observation, err := c.ObserveWorkspaceArchiveData(ctx, operation, source, mappings, maxBytes)
	if err != nil {
		return nil, err
	}
	if observation.CarrierID == "" {
		return nil, fmt.Errorf("original archive carrier is not observed; inspect progress before attempting any bootstrap; no write attempted")
	}
	carrier, err := c.journalCarrier(ctx, observation.CarrierID)
	if err != nil {
		return nil, err
	}
	c.journal.Carrier = *carrier
	plan, err := PlanWorkspaceArchive(c.repo, carrier.ID, operation, source, mappings, maxBytes)
	if err != nil {
		return nil, err
	}
	return c.ResumeWorkspaceArchive(ctx, plan)
}

// VerifiedEntityOperationMappings uses one fresh bulk issue/comment observation
// for every requested operation. A created/updated entity must still exactly
// match its native applied receipt, including the server clock. Observed-only,
// undone, foreign-session and incomplete actions do not become completion proof.
func (c *Client) VerifiedEntityOperationMappings(ctx context.Context, operations map[string]string) (map[string]string, error) {
	return c.entityOperationMappings(ctx, operations, false)
}

// ObservedEntityOperationMappings permits native journal reconciliation while
// preserving its observed attribution. Callers must not call this verified
// execution evidence; the strict mapper above continues to reject observation.
func (c *Client) ObservedEntityOperationMappings(ctx context.Context, operations map[string]string) (map[string]string, error) {
	return c.entityOperationMappings(ctx, operations, true)
}

func (c *Client) entityOperationMappings(ctx context.Context, operations map[string]string, allowObserved bool) (map[string]string, error) {
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
		if _, duplicate := result[action.OperationID]; duplicate {
			return nil, fmt.Errorf("entity operation %s has duplicate native receipts", action.OperationID)
		}
		supportedAction := action.Action == "create" || kind == "board" && action.Action == "board"
		observed := action.Reconciliation != nil || action.UpdateObservation != nil
		if action.Phase != "applied" || action.SessionID != c.journal.SessionID || !strings.EqualFold(action.Author, actor.Login) || action.After == nil || action.Restored != nil || observed && !allowObserved || action.UndoObservation != nil || action.CreationCommand != nil || !supportedAction {
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
		expected := action.After
		if action.Reconciliation != nil {
			if action.Reconciliation.SessionID != c.journal.SessionID || !strings.EqualFold(action.Reconciliation.Actor, actor.Login) {
				return nil, fmt.Errorf("entity reconciliation belongs to another observer")
			}
			expected = action.Reconciliation.State
		}
		if action.UpdateObservation != nil {
			if action.UpdateObservation.SessionID != c.journal.SessionID || !strings.EqualFold(action.UpdateObservation.Actor, actor.Login) {
				return nil, fmt.Errorf("entity observation belongs to another observer")
			}
			expected = action.UpdateObservation.State
		}
		if expected == nil || state.Revision != expected.Revision {
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

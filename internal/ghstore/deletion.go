package ghstore

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"
	"time"
)

// SetDeletedObserved changes only td visibility, never GitHub native state.
// It retains all workflow fields and records the actual actor in shared history.
// As with UpdateObserved, conflict detection is best-effort, not atomic PATCH.
func (c *Client) SetDeletedObserved(ctx context.Context, observed *Record, deleted bool, actor, reason string) (*Record, bool, error) {
	if observed == nil || observed.repository != c.repo || observed.revision == ([32]byte{}) {
		return nil, false, fmt.Errorf("deletion requires an observation from this repository")
	}
	if strings.TrimSpace(actor) == "" {
		return nil, false, &WorkflowInputError{Reason: "deletion/restore requires a session"}
	}
	current, err := c.GetIncludingDeleted(ctx, observed.ID)
	if err != nil {
		return nil, false, err
	}
	if current.revision != observed.revision {
		return nil, false, &ConflictError{ID: observed.ID}
	}
	if (observed.DeletedAt != nil) == deleted {
		return observed, true, nil
	}
	details, err := observed.CopyDetails()
	if err != nil {
		return nil, false, err
	}
	now := time.Now().UTC()
	action := "restore"
	if deleted {
		details.DeletedAt = &now
		action = "delete"
	} else {
		details.DeletedAt = nil
	}
	details.Transitions = append(details.Transitions, TransitionRecord{OperationID: "td-op-" + rand.Text(), Action: action, From: observed.Status, To: observed.Status, SessionID: actor, Reason: reason, At: now})
	result, err := c.UpdateObserved(ctx, observed, Changes{Details: &details})
	return result, false, err
}

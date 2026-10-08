package ghstore

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/workflow"
)

// TransitionRecord records the actor separately from sessions whose claims
// were released. It is shared, editable issue metadata, not a distributed lock.
type TransitionRecord struct {
	OperationID string              `json:"operation_id"`
	Action      string              `json:"action"`
	From        models.Status       `json:"from"`
	To          models.Status       `json:"to"`
	SessionID   string              `json:"session_id"`
	Reason      string              `json:"reason,omitempty"`
	At          time.Time           `json:"at"`
	Snapshot    *models.GitSnapshot `json:"snapshot,omitempty"`
}

type TransitionOptions struct {
	SessionID string
	Reason    string
	Force     bool
	Snapshot  *models.GitSnapshot
}

// CopyDetails gives policy callers a detached value to mutate without changing
// the observed record or its nested history. Missing fields use effective state.
func (r *Record) CopyDetails() (IssueDetails, error) {
	if r.Details == nil {
		return detailsFromIssue(&r.Issue), nil
	}
	data, err := json.Marshal(r.Details)
	if err != nil {
		return IssueDetails{}, err
	}
	var details IssueDetails
	if err := json.Unmarshal(data, &details); err != nil {
		return IssueDetails{}, err
	}
	return details, nil
}

func (c *Client) Transition(ctx context.Context, id, action string, options TransitionOptions) (*Record, bool, error) {
	if strings.TrimSpace(options.SessionID) == "" {
		return nil, false, fmt.Errorf("transition requires a session")
	}
	observed, err := c.Get(ctx, id)
	if err != nil {
		return nil, false, err
	}
	details, err := observed.CopyDetails()
	if err != nil {
		return nil, false, err
	}
	from := observed.Status
	target := models.StatusOpen
	switch action {
	case "start":
		target = models.StatusInProgress
		if from == target {
			if observed.ImplementerSession == options.SessionID {
				return observed, true, nil
			}
			return nil, false, fmt.Errorf("cannot start %s: already in_progress under session %q; release the claim explicitly before starting", observed.ID, observed.ImplementerSession)
		}
		if from == models.StatusBlocked && !options.Force {
			return nil, false, fmt.Errorf("cannot start blocked issue %s without --force", observed.ID)
		}
	case "unstart":
		if from == models.StatusOpen && observed.ImplementerSession == "" {
			return observed, true, nil
		}
		if from != models.StatusInProgress && from != models.StatusOpen {
			return nil, false, fmt.Errorf("cannot unstart %s: status is %s", observed.ID, from)
		}
	case "block":
		target = models.StatusBlocked
		if from == target {
			return observed, true, nil
		}
	case "unblock":
		if from == models.StatusOpen {
			return observed, true, nil
		}
		if from != models.StatusBlocked {
			return nil, false, fmt.Errorf("cannot unblock %s: status is %s", observed.ID, from)
		}
	default:
		return nil, false, fmt.Errorf("unsupported GitHub transition %q", action)
	}
	if from != target && !workflow.DefaultMachine().IsValidTransition(from, target) {
		return nil, false, fmt.Errorf("cannot %s %s: invalid transition from %s", action, observed.ID, from)
	}
	now := time.Now().UTC()
	// Use effective attribution when native state no longer matches the details.
	details.Status = target
	details.ImplementerSession = observed.ImplementerSession
	details.ReviewerSession = ""
	details.ReviewedAt = nil
	details.ReviewRequestedBySession = ""
	details.ClosedBySession = ""
	for i := range details.Reviews {
		if details.Reviews[i].SupersededAt == nil {
			details.Reviews[i].SupersededAt = &now
		}
	}
	history := func(session string, action models.IssueSessionAction) {
		if session != "" {
			details.Sessions = append(details.Sessions, models.IssueSessionHistory{ID: "hist-" + rand.Text(), IssueID: observed.ID, SessionID: session, Action: action, CreatedAt: now})
		}
	}
	switch action {
	case "start":
		if details.ImplementerSession != "" && details.ImplementerSession != options.SessionID {
			history(details.ImplementerSession, models.ActionSessionUnstarted)
		}
		details.ImplementerSession = options.SessionID
		history(options.SessionID, models.ActionSessionStarted)
	case "unstart", "unblock":
		history(details.ImplementerSession, models.ActionSessionUnstarted)
		details.ImplementerSession = ""
	}
	operation := "td-op-" + rand.Text()
	var snapshot *models.GitSnapshot
	if options.Snapshot != nil {
		copy := *options.Snapshot
		copy.IssueID = observed.ID
		copy.Event = action
		snapshot = &copy
	}
	details.Transitions = append(details.Transitions, TransitionRecord{OperationID: operation, Action: action, From: from, To: target, SessionID: options.SessionID, Reason: options.Reason, At: now, Snapshot: snapshot})
	native := nativeStatus(target)
	result, err := c.UpdateObserved(ctx, observed, Changes{Details: &details, Status: &native, Reason: &options.Reason})
	if err != nil {
		return nil, false, fmt.Errorf("%s %s (operation %s): %w", action, observed.ID, operation, err)
	}
	return result, false, nil
}

package ghstore

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/reviewpolicy"
	"github.com/marcus/td/internal/workflow"
)

// TransitionRecord records the actor separately from sessions whose claims
// were released. It is shared, editable issue metadata, not a distributed lock.
type TransitionRecord struct {
	RelatedIssueID     string              `json:"related_issue_id,omitempty"`
	AdminReason        string              `json:"admin_reason,omitempty"`
	SelfCloseException string              `json:"self_close_exception,omitempty"`
	OperationID        string              `json:"operation_id"`
	Action             string              `json:"action"`
	From               models.Status       `json:"from"`
	To                 models.Status       `json:"to"`
	SessionID          string              `json:"session_id"`
	Reason             string              `json:"reason,omitempty"`
	At                 time.Time           `json:"at"`
	Snapshot           *models.GitSnapshot `json:"snapshot,omitempty"`
}

// WorkflowStateError reports a transition incompatible with current state.
type WorkflowStateError struct{ Reason string }

func (e *WorkflowStateError) Error() string { return e.Reason }
func workflowStateError(format string, args ...any) error {
	return &WorkflowStateError{Reason: fmt.Sprintf(format, args...)}
}

type TransitionOptions struct {
	AgentType                                             string
	expectedRevision                                      *[32]byte
	skipReviewHandoff                                     bool
	Mode                                                  reviewpolicy.Mode
	Minor, RecordOnly, SelfReview                         bool
	ReviewedBy, Decision, AdminReason, SelfCloseException string
	SessionID                                             string
	Reason                                                string
	Force                                                 bool
	Snapshot                                              *models.GitSnapshot
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

// TransitionObserved preserves the caller's revision across policy evaluation.
// It is used by HTTP If-Match and other callers that validated a specific read.
// This is optimistic conflict detection, not a conditional GitHub PATCH.
func (c *Client) TransitionObserved(ctx context.Context, observed *Record, action string, options TransitionOptions) (*Record, bool, error) {
	if observed == nil || observed.repository != c.repo || observed.revision == ([32]byte{}) {
		return nil, false, fmt.Errorf("transition observation from this repository is required")
	}
	revision := observed.revision
	options.expectedRevision = &revision
	return c.Transition(ctx, observed.ID, action, options)
}

func (c *Client) Transition(ctx context.Context, id, action string, options TransitionOptions) (*Record, bool, error) {
	if strings.TrimSpace(options.SessionID) == "" {
		return nil, false, fmt.Errorf("transition requires a session")
	}
	if action == "review" || action == "approve" || action == "reject" || action == "close" || action == "reopen" {
		return c.reviewTransition(ctx, id, action, options)
	}
	observed, err := c.Get(ctx, id)
	if err != nil {
		return nil, false, err
	}
	if options.expectedRevision != nil && observed.revision != *options.expectedRevision {
		return nil, false, &ConflictError{ID: observed.ID}
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
			return nil, false, workflowStateError("cannot start %s: already in_progress under session %q; release the claim explicitly before starting", observed.ID, observed.ImplementerSession)
		}
		if from == models.StatusBlocked && !options.Force {
			return nil, false, workflowStateError("cannot start blocked issue %s without --force", observed.ID)
		}
	case "unstart":
		if from == models.StatusOpen && observed.ImplementerSession == "" {
			return observed, true, nil
		}
		if from != models.StatusInProgress && from != models.StatusOpen {
			return nil, false, workflowStateError("cannot unstart %s: status is %s", observed.ID, from)
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
			return nil, false, workflowStateError("cannot unblock %s: status is %s", observed.ID, from)
		}
	default:
		return nil, false, fmt.Errorf("unsupported GitHub transition %q", action)
	}
	if from != target && !workflow.DefaultMachine().IsValidTransition(from, target) {
		return nil, false, workflowStateError("cannot %s %s: invalid transition from %s", action, observed.ID, from)
	}
	now := time.Now().UTC()
	// Use effective attribution when native state no longer matches the details.
	details.Status = target
	details.ReviewBasis = ""
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

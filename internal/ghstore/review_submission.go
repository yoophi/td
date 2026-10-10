package ghstore

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/workflow"
)

// A review request is prepared before its handoff can be created. Only the
// actual handoff identity and timestamp are bound later; actor, contents,
// transition identity, review clock and native event baseline remain fixed.
type reviewSubmissionInput struct {
	Before          ActionIssueState
	Details         IssueDetails
	Reason          string
	Events          []nativeStateEvent
	RequiresHandoff bool
}

func (c *Client) prepareReviewSubmission(ctx context.Context, observed *Record, o TransitionOptions) (*reviewSubmissionInput, error) {
	if observed == nil || observed.repository != c.repo || observed.revision == ([32]byte{}) || strings.TrimSpace(o.SessionID) == "" {
		return nil, fmt.Errorf("review submission requires an observed task and actor")
	}
	if err := ValidateReviewOptions("review", o); err != nil {
		return nil, err
	}
	if !workflow.DefaultMachine().IsValidTransition(observed.Status, models.StatusInReview) {
		return nil, workflowStateError("cannot review %s from %s; start a new review cycle explicitly", observed.ID, observed.Status)
	}
	d, err := observed.CopyDetails()
	if err != nil {
		return nil, err
	}
	before := reviewSubmissionState(observed)
	events, err := c.readStateEvents(ctx, observed.ID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if o.Minor {
		d.Minor = true
	}
	d.ReviewHandoffID, d.ReviewHandoffUpdatedAt = "", time.Time{}
	supersedeReviews(&d, now)
	if observed.ImplementerSession == "" {
		d.ImplementerSession = o.SessionID
		d.Sessions = append(d.Sessions, models.IssueSessionHistory{ID: "hist-" + rand.Text(), IssueID: observed.ID,
			SessionID: o.SessionID, Action: models.ActionSessionStarted, CreatedAt: now})
	} else {
		d.ImplementerSession = observed.ImplementerSession
	}
	d.ReviewRequestedBySession, d.ClosedBySession = o.SessionID, ""
	d.ReviewBasis, d.ReviewEvents = reviewBasis(observed, d), stateEventDigest(events)
	d.Status = models.StatusInReview
	d.Transitions = append(d.Transitions, TransitionRecord{OperationID: "td-op-" + rand.Text(), Action: "review",
		From: observed.Status, To: d.Status, SessionID: o.SessionID, Reason: o.Reason, At: now})
	return &reviewSubmissionInput{Before: before, Details: d, Reason: o.Reason, Events: events,
		RequiresHandoff: !d.Minor && !o.skipReviewHandoff}, nil
}

// reviewSubmissionChanges binds contents only; callers separately fence actual
// source and native event observations before publishing a target mutation.
func reviewSubmissionChanges(observed *Record, input *reviewSubmissionInput, handoff *models.Activity) (Changes, error) {
	// Detach the template so binding a result cannot rewrite the original plan.
	data, err := json.Marshal(input.Details)
	if err != nil {
		return Changes{}, err
	}
	var details IssueDetails
	if err := json.Unmarshal(data, &details); err != nil {
		return Changes{}, err
	}
	if input.RequiresHandoff {
		if handoff == nil || handoff.Kind != "handoff" || handoff.IssueID != observed.ID || handoff.UpdatedAt.IsZero() {
			return Changes{}, fmt.Errorf("review submission requires its actual handoff result")
		}
		if _, err := CommentNumber(handoff.ID); err != nil {
			return Changes{}, err
		}
		details.ReviewHandoffID, details.ReviewHandoffUpdatedAt = handoff.ID, handoff.UpdatedAt
	}
	native := models.StatusOpen
	return Changes{Details: &details, Status: &native, Reason: &input.Reason}, nil
}

func (c *Client) materializeReviewSubmission(ctx context.Context, observed *Record, input *reviewSubmissionInput, handoff *models.Activity) (*issueUpdatePlan, error) {
	state := reviewSubmissionState(observed)
	if !sameActionContents(input.Before, state) || state.UpdatedAt.Before(input.Before.UpdatedAt) {
		return nil, &ConflictError{ID: observed.ID}
	}
	change, err := reviewSubmissionChanges(observed, input, handoff)
	if err != nil {
		return nil, err
	}
	events, err := c.readStateEvents(ctx, observed.ID)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(events, input.Events) {
		return nil, fmt.Errorf("review submission native state events changed: %w", &ConflictError{ID: observed.ID})
	}
	return c.prepareIssueUpdate(ctx, observed, change)
}

// Non-journaled review callers need content conflict checks but do not acquire
// the stricter archive schema contract. Journal writers validate their own
// complete ActionIssueState, including the native clock, before publication.
func reviewSubmissionState(record *Record) ActionIssueState {
	state := ActionIssueState{Title: record.Title, Body: record.nativeBody, State: record.nativeState,
		Labels: append([]string{}, record.Labels...), UpdatedAt: record.UpdatedAt}
	state.Revision, _ = state.checksum()
	return state
}

func (c *Client) submitReview(ctx context.Context, observed *Record, options TransitionOptions) (*Record, bool, error) {
	input, err := c.prepareReviewSubmission(ctx, observed, options)
	if err != nil {
		return nil, false, err
	}
	var handoff *models.Activity
	if input.RequiresHandoff {
		observed, handoff, err = c.reviewHandoff(ctx, observed, options)
		if err != nil {
			return nil, false, err
		}
	}
	plan, err := c.materializeReviewSubmission(ctx, observed, input, handoff)
	if err != nil {
		return nil, false, err
	}
	result, err := c.writeIssueUpdate(ctx, plan)
	if err != nil {
		operation := input.Details.Transitions[len(input.Details.Transitions)-1].OperationID
		return nil, false, fmt.Errorf("review %s (operation %s): %w", observed.ID, operation, err)
	}
	return result, false, nil
}

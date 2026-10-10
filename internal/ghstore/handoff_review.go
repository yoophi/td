package ghstore

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/marcus/td/internal/models"
)

// PreparedHandoffReview keeps requested inputs separate from actual GitHub
// results. It is deliberately opaque: callers cannot replace its selected task,
// review intent or published handoff between the two effects.
// Persistent command-wide recovery/undo is a separate journal integration.
type PreparedHandoffReview struct {
	client           *Client
	observed         *Record
	data             activityData
	body             string
	review           *reviewSubmissionInput
	commentAttempted bool
	reviewAttempted  bool
	published        *journaledActivityResult
}

// PrepareHandoffReview validates and detaches every selected input before any
// comment, label or journal write. Non-in-progress tasks keep their handoff but
// are never silently promoted into the review selection by a later reread.
func (c *Client) PrepareHandoffReview(ctx context.Context, inputs []ActivityGroupInput, options TransitionOptions) ([]*PreparedHandoffReview, error) {
	if len(inputs) == 0 {
		return nil, fmt.Errorf("handoff --review requires at least one tagged issue")
	}
	if err := ValidateReviewOptions("review", options); err != nil {
		return nil, err
	}
	prepared := make([]*PreparedHandoffReview, 0, len(inputs))
	expected := map[string]ActionIssueState{}
	operation, workSession := inputs[0].Activity.OperationID, inputs[0].Activity.WorkSessionID
	for _, input := range inputs {
		r := input.Issue
		if r == nil || r.repository != c.repo || r.revision == ([32]byte{}) || r.DeletedAt != nil ||
			r.meta.EntityKind != "" && r.meta.EntityKind != "issue" ||
			input.Activity.Kind != "handoff" || input.Activity.SessionID != options.SessionID ||
			options.SessionID == "" || operation == "" || workSession == "" ||
			input.Activity.OperationID != operation || input.Activity.WorkSessionID != workSession {
			return nil, fmt.Errorf("handoff review requires live observations and one complete work-session operation; no write attempted")
		}
		if _, duplicate := expected[r.ID]; duplicate {
			return nil, fmt.Errorf("handoff review repeats %s; no write attempted", r.ID)
		}
		before, err := actionIssueState(r)
		if err != nil {
			return nil, err
		}
		_, body, err := prepareActivityInput(r.ID, input.Activity)
		if err != nil {
			return nil, err
		}
		// Decode owned values; input slices, Details and Snapshot belong to callers.
		activity, err := decodeActivity(apiComment{ID: 1, Body: body}, r.ID)
		if err != nil {
			return nil, err
		}
		data, _, err := prepareActivityInput(r.ID, activity)
		if err != nil {
			return nil, err
		}
		observed, err := detachHandoffObservation(r)
		if err != nil {
			return nil, err
		}
		p := &PreparedHandoffReview{client: c, observed: observed, data: data, body: body}
		if observed.Status == models.StatusInProgress {
			p.review, err = c.prepareReviewSubmission(ctx, observed, options)
			if err != nil {
				return nil, err
			}
		}
		if err := c.checkHandoffReviewCapacity(ctx, p, before); err != nil {
			return nil, fmt.Errorf("handoff review preflight for %s (no write attempted): %w", r.ID, err)
		}
		prepared = append(prepared, p)
		expected[r.ID] = before
	}
	if err := c.verifyGroupExpected(ctx, expected); err != nil {
		return nil, err
	}
	return prepared, nil
}

func detachHandoffObservation(original *Record) (*Record, error) {
	data, err := json.Marshal(original)
	if err != nil {
		return nil, err
	}
	var observed Record
	if err := json.Unmarshal(data, &observed); err != nil {
		return nil, err
	}
	_, observed.meta, observed.managed, err = decodeBody(original.nativeBody)
	if err != nil {
		return nil, err
	}
	observed.Details = observed.meta.Details
	observed.nativeBody, observed.nativeState = original.nativeBody, original.nativeState
	// The revision includes native fields absent from an archive state (for
	// example GitHub's issue author). Keep the actual observed revision instead
	// of reconstructing one from only title/body/state/labels and update time.
	observed.revision, observed.repository = original.revision, original.repository
	return &observed, nil
}

func (c *Client) checkHandoffReviewCapacity(ctx context.Context, p *PreparedHandoffReview, before ActionIssueState) error {
	carrier := "gh-2147483647"
	if carrier == p.observed.ID {
		carrier = "gh-2147483646"
	}
	comment := ActionJournal{OperationID: "probe-comment", SessionID: p.data.SessionID, IssueID: p.observed.ID,
		CarrierID: carrier, Action: "comment_create", Phase: "prepared", Before: &before,
		CommentInput: &ActionCommentInput{Body: p.body, OperationID: p.data.OperationID}}
	if err := checkActivityGroupMemberCapacity(comment); err != nil {
		return err
	}
	if p.review == nil {
		return nil
	}
	// These placeholders exist only in a sizing probe. They never enter the
	// requested template, a stored receipt, or a target write.
	handoff := &models.Activity{ID: "ghc-9223372036854775807", IssueID: p.observed.ID, Kind: "handoff",
		UpdatedAt: time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC)}
	change, err := reviewSubmissionChanges(p.observed, p.review, handoff)
	if err != nil {
		return err
	}
	plan, err := c.prepareIssueUpdate(ctx, p.observed, change)
	if err != nil {
		return err
	}
	target, err := updateInputFromPayload(before, plan.payload)
	if err != nil {
		return err
	}
	events := slices.Clone(p.review.Events)
	return checkUpdateArchiveCapacity(ActionJournal{OperationID: "probe-review", SessionID: p.data.SessionID,
		IssueID: p.observed.ID, CarrierID: carrier, Action: "update", Phase: "prepared", Before: &before,
		UpdateIntent: &ActionUpdateIntent{Target: target, NativeEvents: &events}})
}

func (c *Client) checkPreparedHandoff(p *PreparedHandoffReview) error {
	if p == nil || p.client != c || c.journal == nil || c.journal.SessionID != p.data.SessionID {
		return fmt.Errorf("prepared handoff requires its original client and tracked session")
	}
	return nil
}

// AppendPreparedHandoff publishes the frozen activity against its original
// observation. An uncertain attempt cannot be replayed through this plan.
func (c *Client) AppendPreparedHandoff(ctx context.Context, p *PreparedHandoffReview) error {
	if err := c.checkPreparedHandoff(p); err != nil {
		return err
	}
	if p.commentAttempted {
		return fmt.Errorf("handoff was already attempted; inspect its journal before retrying")
	}
	p.commentAttempted = true
	result, err := c.appendJournaledActivityResult(ctx, p.observed, p.data, p.body)
	if err != nil {
		return err
	}
	p.published = result
	return nil
}

// SubmitPreparedHandoffReview binds only the verified comment from this plan.
// It never searches for the latest handoff or adopts a changed task revision.
func (c *Client) SubmitPreparedHandoffReview(ctx context.Context, p *PreparedHandoffReview) (bool, error) {
	if err := c.checkPreparedHandoff(p); err != nil {
		return false, err
	}
	if p.published == nil || p.reviewAttempted {
		return false, fmt.Errorf("handoff review requires one verified, not previously submitted handoff")
	}
	p.reviewAttempted = true
	actual := p.published
	if err := c.readNativeComment(ctx, actual.Comment); err != nil {
		return false, err
	}
	current, err := c.Get(ctx, p.observed.ID)
	if err != nil {
		return false, err
	}
	if current.revision != actual.Issue.revision {
		return false, &ConflictError{ID: current.ID}
	}
	if p.review == nil {
		return false, nil
	}
	plan, err := c.materializeReviewSubmission(ctx, current, p.review, actual.Activity)
	if err != nil {
		return false, err
	}
	_, err = c.writeIssueUpdate(ctx, plan)
	return err == nil, err
}

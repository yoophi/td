package ghstore

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"
	"time"

	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/reviewpolicy"
)

// The same preparation is used for standalone and grouped rejections, so a
// batch cannot silently adopt a later review or regenerate its attribution.
func (c *Client) prepareRejection(ctx context.Context, observed *Record, o TransitionOptions) (*issueUpdatePlan, error) {
	if observed == nil || observed.repository != c.repo || observed.revision == ([32]byte{}) || strings.TrimSpace(o.SessionID) == "" {
		return nil, fmt.Errorf("rejection requires an observed task and actor")
	}
	if err := ValidateReviewOptions("reject", o); err != nil {
		return nil, err
	}
	if observed.Status == models.StatusOpen {
		return &issueUpdatePlan{observed: observed, payload: map[string]any{}, noop: true}, nil
	}
	if observed.Status != models.StatusInReview {
		return nil, workflowStateError("cannot reject %s: status is %s", observed.ID, observed.Status)
	}
	d, err := observed.CopyDetails()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	supersedeReviews(&d, now)
	d.Status = models.StatusOpen
	d.ImplementerSession, d.ReviewRequestedBySession, d.ClosedBySession, d.ReviewBasis = "", "", "", ""
	d.Reviews = append(d.Reviews, models.IssueReview{ID: "rv-" + rand.Text(), IssueID: observed.ID,
		ReviewerSession: o.SessionID, Decision: reviewpolicy.DecisionChangesRequested, Summary: o.Reason, CreatedAt: now})
	d.Sessions = append(d.Sessions, models.IssueSessionHistory{ID: "hist-" + rand.Text(), IssueID: observed.ID,
		SessionID: o.SessionID, Action: models.ActionSessionReviewChangesRequested, CreatedAt: now})
	d.Transitions = append(d.Transitions, TransitionRecord{OperationID: "td-op-" + rand.Text(), Action: "reject",
		From: observed.Status, To: d.Status, SessionID: o.SessionID, Reason: o.Reason, At: now})
	native := models.StatusOpen
	return c.prepareIssueUpdate(ctx, observed, Changes{Details: &d, Status: &native, Reason: &o.Reason})
}

package ghstore

import (
	"context"
	"errors"
	"fmt"
	"github.com/marcus/td/internal/models"
	"strings"
)

// MonitorReviewFacts is an observation for queue classification, not a grant
// to approve or close. Mutations must run their own policy and revision checks.
type MonitorReviewFacts struct {
	ImplementationInvolved bool
	AnyInvolved            bool
	ActiveApproval         bool
	Fresh                  bool
}

func (c *Client) ObserveMonitorReview(ctx context.Context, observed *Record, session string) (*MonitorReviewFacts, error) {
	if observed == nil || observed.repository != c.repo || observed.revision == ([32]byte{}) || strings.TrimSpace(session) == "" || observed.Status != models.StatusInReview || observed.DeletedAt != nil {
		return nil, fmt.Errorf("monitor review requires an observed in-review issue and actual session")
	}
	details, err := observed.CopyDetails()
	if err != nil {
		return nil, err
	}
	any, implemented, _ := participation(observed, details, session)
	facts := &MonitorReviewFacts{ImplementationInvolved: implemented, AnyInvolved: any}
	_, err = c.verifyReview(ctx, observed, details)
	var stale *WorkflowStateError
	if err != nil && !errors.As(err, &stale) {
		return nil, err
	}
	facts.Fresh = err == nil
	facts.ActiveApproval = facts.Fresh && activeApproval(&details) != nil
	current, err := c.Get(ctx, observed.ID)
	if err != nil {
		return nil, err
	}
	if current.revision != observed.revision {
		return nil, &ConflictError{ID: observed.ID}
	}
	return facts, nil
}

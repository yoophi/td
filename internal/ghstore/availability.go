package ghstore

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/reviewpolicy"
)

// AvailableTransitions reports actions reachable through ordinary policy. An
// action can still require input (reason or honest review acknowledgement).
// Administrative exceptions and forced starts are not advertised. It never
// writes, persists a probe attribution, or authorizes a subsequent mutation.
func (c *Client) AvailableTransitions(ctx context.Context, observed *Record, session string, mode reviewpolicy.Mode) ([]string, error) {
	if observed == nil || observed.repository != c.repo || observed.revision == ([32]byte{}) || strings.TrimSpace(session) == "" {
		return nil, fmt.Errorf("availability requires an observed issue and actual session")
	}
	o := TransitionOptions{SessionID: session, Mode: mode}
	if err := ValidateReviewOptions("approve", o); err != nil {
		return nil, err
	}
	d, err := observed.CopyDetails()
	if err != nil {
		return nil, err
	}
	actions := []string{}
	status := observed.Status
	if status == models.StatusOpen {
		actions = append(actions, "start")
	}
	if status == models.StatusOpen || status == models.StatusInProgress {
		actions = append(actions, "review")
	}
	active := activeApproval(&d) != nil && (mode == reviewpolicy.ModeTrusted || mode == reviewpolicy.ModeDelegated)
	validReview := false
	if status == models.StatusInReview {
		_, err := c.verifyReview(ctx, observed, d)
		var stale *WorkflowStateError
		if err != nil && !errors.As(err, &stale) {
			return nil, err
		}
		validReview = err == nil
		eligible := reviewerEligibility(observed, d, o).Allowed
		if !eligible && mode == reviewpolicy.ModeTrusted {
			// Only test reachability, as the SQLite HTTP path does. This value
			// is never used in a write or offered as actual reviewer identity.
			probe := o
			probe.ReviewedBy = "availability probe"
			eligible = reviewerEligibility(observed, d, probe).Allowed
		}
		if validReview && (active || eligible) {
			actions = append(actions, "approve")
		}
		actions = append(actions, "reject")
	}
	if status == models.StatusOpen || status == models.StatusInProgress {
		actions = append(actions, "block")
	}
	if status == models.StatusBlocked {
		actions = append(actions, "unblock")
	}
	closeAllowed := closeWithoutApprovalAllowed(observed, d, o)
	if status == models.StatusInReview {
		if active {
			closeAllowed = validReview
		} else {
			closeAllowed = observed.Minor && closeAllowed
		}
	}
	if status != models.StatusClosed && closeAllowed {
		actions = append(actions, "close")
	}
	if status == models.StatusClosed {
		actions = append(actions, "reopen")
	}
	current, err := c.Get(ctx, observed.ID)
	if err != nil {
		return nil, err
	}
	if current.revision != observed.revision {
		return nil, &ConflictError{ID: observed.ID}
	}
	return actions, nil
}

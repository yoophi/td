package ghstore

import (
	"context"
	"fmt"

	"github.com/marcus/td/internal/models"
)

// ReleaseObservedClaim preserves the preview's revision through the ordinary
// transition checks. A newly active or reassigned claim cannot be silently
// substituted by a fresh Get. This is best-effort, not conditional GitHub PATCH.
func (c *Client) ReleaseObservedClaim(ctx context.Context, observed *Record, options TransitionOptions) (*Record, error) {
	if observed == nil || observed.repository != c.repo || observed.revision == ([32]byte{}) {
		return nil, fmt.Errorf("claim observation from this repository is required")
	}
	if observed.ImplementerSession == "" || (observed.Status != models.StatusInProgress && observed.Status != models.StatusOpen) {
		return nil, fmt.Errorf("%s is not a releasable claim", observed.ID)
	}
	revision := observed.revision
	options.expectedRevision = &revision
	record, _, err := c.Transition(ctx, observed.ID, "unstart", options)
	return record, err
}

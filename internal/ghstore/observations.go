package ghstore

import (
	"context"
	"fmt"
)

// ReadObserved verifies a prior private observation without writing. Callers
// must retain that observation for later writes; this is not an atomic lock.
func (c *Client) ReadObserved(ctx context.Context, observed *Record) (*Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if observed == nil || observed.repository != c.repo || observed.revision == ([32]byte{}) {
		return nil, fmt.Errorf("read requires an observation from this repository")
	}
	n, err := Number(observed.ID)
	if err != nil || n != observed.Number || observed.ID != fmt.Sprintf("gh-%d", n) {
		return nil, fmt.Errorf("observed issue identity is inconsistent")
	}
	current, err := c.Get(ctx, observed.ID)
	if err != nil {
		return nil, err
	}
	if current.revision != observed.revision {
		return nil, &ConflictError{ID: observed.ID}
	}
	return current, nil
}

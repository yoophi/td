package ghstore

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/reviewpolicy"
)

// ApprovalCandidates selects by status and participation policy only. Every
// candidate is re-read and its review basis/events/handoff revalidated by
// Transition before any approval write. A stale review is an error, not a
// silently omitted candidate. Listing candidates never writes or grants claims.
func (c *Client) ApprovalCandidates(ctx context.Context, options TransitionOptions) ([]Record, error) {
	if strings.TrimSpace(options.SessionID) == "" {
		return nil, fmt.Errorf("approval selection requires a session")
	}
	if err := ValidateReviewOptions("approve", options); err != nil {
		return nil, err
	}
	records, err := c.List(ctx, false)
	if err != nil {
		return nil, err
	}
	selected := make([]Record, 0)
	seen := map[int]bool{}
	for _, record := range records {
		if seen[record.Number] {
			return nil, fmt.Errorf("approval candidate pagination repeated %s; retry the read", record.ID)
		}
		seen[record.Number] = true
		if record.Status != models.StatusInReview {
			continue
		}
		details, err := record.CopyDetails()
		if err != nil {
			return nil, err
		}
		readyToClose := false
		if !options.RecordOnly && (options.Mode == reviewpolicy.ModeTrusted || options.Mode == reviewpolicy.ModeDelegated) {
			for _, review := range details.Reviews {
				if review.SupersededAt == nil && review.Decision == reviewpolicy.DecisionApproved {
					readyToClose = true
				}
			}
		}
		if readyToClose || reviewerEligibility(&record, details, options).Allowed {
			selected = append(selected, record)
		}
	}
	slices.SortFunc(selected, func(a, b Record) int { return a.Number - b.Number })
	return selected, nil
}

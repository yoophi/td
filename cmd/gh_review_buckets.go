package cmd

import (
	"context"
	"fmt"

	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/reviewpolicy"
)

type gitHubReviewObserver interface {
	ObserveMonitorReview(context.Context, *ghstore.Record, string) (*ghstore.MonitorReviewFacts, error)
}

func readGitHubReviewBuckets(ctx context.Context, reader gitHubReviewObserver, records []ghstore.Record, actor string, mode reviewpolicy.Mode) (map[string]bool, map[string]bool, error) {
	if actor == "" {
		return nil, nil, fmt.Errorf("review queue requires an actual session")
	}
	if _, err := reviewpolicy.ParseMode(string(mode)); err != nil {
		return nil, nil, err
	}
	awaiting, ready := map[string]bool{}, map[string]bool{}
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if record.Status != models.StatusInReview || record.DeletedAt != nil {
			continue
		}
		facts, err := reader.ObserveMonitorReview(ctx, &record, actor)
		if err != nil {
			return nil, nil, fmt.Errorf("review queue for %s: %w", record.ID, err)
		}
		if facts == nil || !facts.Fresh {
			return nil, nil, fmt.Errorf("%s review snapshot is stale or unavailable; refresh the handoff and request review again", record.ID)
		}
		if facts.ActiveApproval {
			if mode == reviewpolicy.ModeTrusted || mode == reviewpolicy.ModeDelegated {
				ready[record.ID] = true
			}
			continue
		}
		if record.ImplementerSession == "" {
			continue
		}
		decision := reviewpolicy.EvaluateReviewerEligibility(reviewpolicy.ReviewerEligibilityInput{Mode: mode, Issue: &record.Issue, SessionID: actor, SessionIsImplementer: record.ImplementerSession == actor, SessionIsCreator: record.CreatorSession == actor, HasImplementationHistory: facts.ImplementationInvolved, WasAnyInvolved: facts.AnyInvolved})
		if mode == reviewpolicy.ModeTrusted || decision.Allowed {
			awaiting[record.ID] = true
		}
	}
	return awaiting, ready, nil
}

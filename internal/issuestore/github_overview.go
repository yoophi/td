package issuestore

import (
	"context"
	"fmt"
	"strings"

	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/reviewpolicy"
)

type GitHubOverviewReader interface {
	List(context.Context, bool) ([]ghstore.Record, error)
	ObserveMonitorReview(context.Context, *ghstore.Record, string) (*ghstore.MonitorReviewFacts, error)
}

type ProjectOverview struct {
	Issues      map[string]int `json:"issues"`
	ByType      map[string]int `json:"by_type"`
	ByPriority  map[string]int `json:"by_priority"`
	ReviewQueue map[string]int `json:"review_queue"`
}

// ReadGitHubOverview uses a complete visible issue listing and verified review
// observations. Counts are informational, not authorization for a later write.
func ReadGitHubOverview(ctx context.Context, reader GitHubOverviewReader, actor string, mode reviewpolicy.Mode) (*ProjectOverview, error) {
	if strings.TrimSpace(actor) == "" {
		return nil, fmt.Errorf("overview requires an actual session")
	}
	if _, err := reviewpolicy.ParseMode(string(mode)); err != nil {
		return nil, err
	}
	records, err := reader.List(ctx, true)
	if err != nil {
		return nil, err
	}
	r := &ProjectOverview{Issues: map[string]int{"total": 0, "open": 0, "in_progress": 0, "blocked": 0, "in_review": 0, "closed": 0}, ByType: map[string]int{"bug": 0, "feature": 0, "task": 0, "epic": 0, "chore": 0}, ByPriority: map[string]int{"P0": 0, "P1": 0, "P2": 0, "P3": 0, "P4": 0}, ReviewQueue: map[string]int{"awaiting_review": 0, "you_can_review": 0, "you_can_close_after": 0, "stale_review": 0}}
	seen := map[string]bool{}
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if seen[record.ID] {
			return nil, fmt.Errorf("overview listing repeated %s; refuse partial counts", record.ID)
		}
		seen[record.ID] = true
		if record.DeletedAt != nil {
			continue
		}
		r.Issues["total"]++
		r.Issues[string(record.Status)]++
		r.ByType[string(record.Type)]++
		r.ByPriority[string(record.Priority)]++
		if record.Status != models.StatusInReview {
			continue
		}
		r.ReviewQueue["awaiting_review"]++
		facts, err := reader.ObserveMonitorReview(ctx, &record, actor)
		if err != nil {
			return nil, fmt.Errorf("overview review observation for %s: %w", record.ID, err)
		}
		if facts == nil {
			return nil, fmt.Errorf("missing review facts for %s", record.ID)
		}
		if !facts.Fresh {
			r.ReviewQueue["stale_review"]++
			continue
		}
		isImpl, isCreator := record.ImplementerSession == actor, record.CreatorSession == actor
		if facts.ActiveApproval && (mode == reviewpolicy.ModeTrusted || mode == reviewpolicy.ModeDelegated) {
			decision := reviewpolicy.EvaluateCloseEligibility(reviewpolicy.CloseEligibilityInput{Mode: mode, Issue: &record.Issue, SessionID: actor, SessionIsImplementer: isImpl, SessionIsCreator: isCreator, SessionIsReviewerOfRecord: record.ReviewerSession == actor, SessionIsReviewRequester: record.ReviewRequestedBySession == actor, HasImplementationHistory: facts.ImplementationInvolved, WasAnyInvolved: facts.AnyInvolved, HasActiveApproval: true})
			if decision.Allowed {
				r.ReviewQueue["you_can_close_after"]++
			}
			continue
		}
		decision := reviewpolicy.EvaluateReviewerEligibility(reviewpolicy.ReviewerEligibilityInput{Mode: mode, Issue: &record.Issue, SessionID: actor, SessionIsImplementer: isImpl, SessionIsCreator: isCreator, HasImplementationHistory: facts.ImplementationInvolved, WasAnyInvolved: facts.AnyInvolved})
		// Trusted implementation participants may act only with an explicit
		// honest review acknowledgement at mutation time, like SQLite info.
		if decision.Allowed || (mode == reviewpolicy.ModeTrusted && (isImpl || facts.ImplementationInvolved)) {
			r.ReviewQueue["you_can_review"]++
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return r, nil
}

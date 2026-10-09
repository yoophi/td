package cmd

import (
	"context"
	"fmt"
	"slices"

	"github.com/marcus/td/internal/ghcontext"
	"github.com/marcus/td/internal/issuestore"
	"github.com/marcus/td/internal/models"
)

// An ordinary issue log/handoff can belong to an active bundle without the
// issue being tagged. Remember that issue so bundle readers can find its shared
// history later. GitHub is still authoritative; no comment copy is stored here.
type gitHubActivityTracker struct {
	issuestore.ActivityStore
	Scope ghcontext.Scope
}

func (t *gitHubActivityTracker) AppendActivity(ctx context.Context, id string, input models.Activity) (*models.Activity, error) {
	created, err := t.ActivityStore.AppendActivity(ctx, id, input)
	if err != nil || input.WorkSessionID == "" {
		return created, err
	}
	_, err = t.Scope.Update(ctx, func(state *ghcontext.State) error {
		ws, err := state.WorkSession(input.WorkSessionID)
		if err != nil {
			return err
		}
		if ws.SessionID != input.SessionID {
			return fmt.Errorf("work-session actor differs from recorded activity")
		}
		if !slices.Contains(ws.HistoryIssues, created.IssueID) {
			ws.HistoryIssues = append(ws.HistoryIssues, created.IssueID)
		}
		return nil
	})
	if err != nil {
		return created, fmt.Errorf("activity %s was saved on %s (operation %s), but local work-session association failed; inspect the saved comment before retrying: %w", created.ID, created.IssueID, created.OperationID, err)
	}
	return created, nil
}

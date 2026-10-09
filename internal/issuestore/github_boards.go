package issuestore

import (
	"context"
	"fmt"
	"slices"

	"github.com/marcus/td/internal/db"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/query"
)

type GitHubBoardReader interface {
	ActivityStore
	List(context.Context, bool) ([]ghstore.Record, error)
	BoardQueryObserved(*ghstore.BoardRecord) (string, error)
}

// GitHubBoardCandidates evaluates a board's actual TDQ using a complete,
// request-scoped issue snapshot. It never opens SQLite. Empty actor follows the
// existing web board contract, which neutralizes @me. keep only bypasses the
// status filter, never the board query or logical deletion.
type GitHubBoardSnapshot struct {
	Candidates []models.Issue
	Records    []ghstore.Record
}

func GitHubBoardCandidates(ctx context.Context, c GitHubBoardReader, board *ghstore.BoardRecord, includeClosed bool, keep ...string) ([]models.Issue, error) {
	snapshot, err := ReadGitHubBoardSnapshot(ctx, c, board, includeClosed, keep...)
	if err != nil {
		return nil, err
	}
	return snapshot.Candidates, nil
}

// ReadGitHubBoardSnapshot exposes the same listing for dependency summaries,
// avoiding a second listing with inconsistent task metadata.
func ReadGitHubBoardSnapshot(ctx context.Context, c GitHubBoardReader, board *ghstore.BoardRecord, includeClosed bool, keep ...string) (*GitHubBoardSnapshot, error) {
	if board == nil {
		return nil, fmt.Errorf("board is required")
	}
	return ReadGitHubBoardSnapshotForActor(ctx, c, board, "", includeClosed, keep...)
}

// ReadGitHubBoardSnapshotForActor evaluates @me with the caller's actual td
// actor. Web callers retain their established empty-actor contract.
func ReadGitHubBoardSnapshotForActor(ctx context.Context, c GitHubBoardReader, board *ghstore.BoardRecord, actor string, includeClosed bool, keep ...string) (*GitHubBoardSnapshot, error) {
	if board == nil {
		return nil, fmt.Errorf("board is required")
	}
	expression, err := c.BoardQueryObserved(board)
	if err != nil {
		return nil, err
	}
	records, err := c.List(ctx, true)
	if err != nil {
		return nil, err
	}
	snapshot, err := NewGitHubQuerySnapshot(ctx, records, c)
	if err != nil {
		return nil, err
	}
	var matched []models.Issue
	if expression == "" {
		matched, err = snapshot.ListIssues(db.ListIssuesOptions{SortBy: "priority"})
	} else {
		var result query.ExecuteResult
		result, err = query.ExecuteDetailed(snapshot, expression, actor, query.ExecuteOptions{SortBy: "priority", MaxResults: len(records) + 1})
		if err == nil && (result.ScanLimited || result.Truncated) {
			return nil, fmt.Errorf("board query was incomplete; refuse partial board")
		}
		matched = result.Issues
	}
	if err != nil {
		return nil, err
	}
	issues := make([]models.Issue, 0, len(matched))
	for _, i := range matched {
		if includeClosed || i.Status != models.StatusClosed || slices.Contains(keep, i.ID) {
			issues = append(issues, i)
		}
	}
	return &GitHubBoardSnapshot{Candidates: issues, Records: records}, nil
}

package monitor

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/marcus/td/internal/features"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/issuestore"
	"github.com/marcus/td/internal/models"
)

// BoardDataSource returns fully classified cards. Its lifecycle context must
// be cancelled when the owning monitor closes. It does not enable other TUI
// actions; those require the wider monitor store integration.
type BoardDataSource interface {
	ListBoards() ([]models.Board, error)
	LoadBoard(string, map[models.Status]bool) BoardIssuesMsg
	LastViewedBoard() (*models.Board, error)
}
type githubBoardClient interface {
	issuestore.GitHubBoardReader
	ListBoards(context.Context) ([]ghstore.BoardRecord, error)
	GetBoard(context.Context, string) (*ghstore.BoardRecord, error)
	ObserveMonitorReview(context.Context, *ghstore.Record, string) (*ghstore.MonitorReviewFacts, error)
}
type GitHubBoardSource struct {
	preferences    *localMonitorPreferences
	previewMu      sync.Mutex
	previewCancel  context.CancelFunc
	ctx            context.Context
	baseDir, actor string
	open           func(context.Context) (githubBoardClient, error)
}

func NewGitHubBoardSource(ctx context.Context, dir string, selected models.GitHubStoreConfig, actor string) *GitHubBoardSource {
	return &GitHubBoardSource{ctx: ctx, baseDir: dir, actor: actor, open: func(ctx context.Context) (githubBoardClient, error) { return ghstore.Open(ctx, dir, &selected) }}
}
func (s *GitHubBoardSource) ListBoards() ([]models.Board, error) {
	c, err := s.open(s.ctx)
	if err != nil {
		return nil, err
	}
	records, err := c.ListBoards(s.ctx)
	if err != nil {
		return nil, err
	}
	boards := make([]models.Board, 0, len(records))
	for _, r := range records {
		board, err := s.displayBoard(r.Board)
		if err != nil {
			return nil, err
		}
		boards = append(boards, *board)
	}
	return boards, nil
}
func (s *GitHubBoardSource) LastViewedBoard() (*models.Board, error) {
	if s.preferences == nil {
		return nil, nil
	}
	prefs, err := s.preferences.Load()
	if err != nil {
		return nil, err
	}
	if prefs.LastBoardID == "" {
		return nil, nil
	}
	boards, err := s.ListBoards()
	if err != nil {
		return nil, err
	}
	for _, board := range boards {
		if board.ID == prefs.LastBoardID {
			return &board, nil
		}
	}
	// Removed boards do not prevent startup and are not resurrected.
	return nil, nil
}
func (s *GitHubBoardSource) LoadBoard(id string, statuses map[models.Status]bool) BoardIssuesMsg {
	fail := func(err error) BoardIssuesMsg { return BoardIssuesMsg{BoardID: id, Error: err} }
	if strings.TrimSpace(s.actor) == "" {
		return fail(fmt.Errorf("board source requires actual monitor session"))
	}
	mode, err := features.ResolveReviewPolicyMode(s.baseDir)
	if err != nil {
		return fail(err)
	}
	c, err := s.open(s.ctx)
	if err != nil {
		return fail(err)
	}
	b, err := c.GetBoard(s.ctx, id)
	if err != nil {
		return fail(err)
	}
	snapshot, err := issuestore.ReadGitHubBoardSnapshotForActor(s.ctx, c, b, s.actor, true)
	if err != nil {
		return fail(err)
	}
	querySource, err := issuestore.NewGitHubQuerySnapshot(s.ctx, snapshot.Records, c)
	if err != nil {
		return fail(err)
	}
	rejected, err := querySource.GetRejectedInProgressIssueIDs()
	if err != nil {
		return fail(err)
	}
	byID := map[string]ghstore.Record{}
	for _, r := range snapshot.Records {
		if r.DeletedAt == nil {
			byID[r.ID] = r
		}
	}
	candidates := []models.Issue{}
	for _, i := range snapshot.Candidates {
		if statuses[i.Status] {
			candidates = append(candidates, i)
		}
	}
	cards, err := ghstore.ApplyBoardPositions(b, candidates)
	if err != nil {
		return fail(err)
	}
	for i := range cards {
		record := byID[cards[i].Issue.ID]
		category := CategoryReady
		switch record.Status {
		case models.StatusOpen:
			if record.Details != nil {
				for _, dep := range record.Details.Dependencies {
					target, ok := byID[dep]
					if !ok {
						return fail(fmt.Errorf("board dependency %s of %s is missing or deleted", dep, record.ID))
					}
					if target.Status != models.StatusClosed {
						category = CategoryBlocked
					}
				}
			}
		case models.StatusInProgress:
			category = CategoryInProgress
			if rejected[record.ID] {
				category = CategoryNeedsRework
			}
		case models.StatusBlocked:
			category = CategoryBlocked
		case models.StatusClosed:
			category = CategoryClosed
		case models.StatusInReview:
			var facts *ghstore.MonitorReviewFacts
			var err error
			if _, bulk := c.(ghstore.SnapshotReader); bulk {
				facts, err = ghstore.UnverifiedMonitorReview(&record, s.actor)
			} else {
				facts, err = c.ObserveMonitorReview(s.ctx, &record, s.actor)
			}
			if err != nil {
				return fail(err)
			}
			if facts == nil {
				return fail(fmt.Errorf("missing board review observation for %s", record.ID))
			}
			category = CategoryPendingOther
			if facts.Fresh {
				category = CategorizeInReview(&record.Issue, s.actor, mode, facts.ImplementationInvolved, facts.AnyInvolved, facts.ActiveApproval)
			}
		default:
			return fail(fmt.Errorf("invalid board task status %q", record.Status))
		}
		cards[i].Category = string(category)
	}
	// Stable positional order is already applied; never fall back to a DB read.
	display, err := s.displayBoard(b.Board)
	if err != nil {
		return fail(err)
	}
	return BoardIssuesMsg{BoardID: id, Issues: slices.Clone(cards), RejectedIDs: rejected, Board: display, ViewStore: &githubBoardView{source: s, observed: *b}, MoveStore: &githubBoardMove{source: s, observed: *b, candidates: slices.Clone(candidates)}}
}

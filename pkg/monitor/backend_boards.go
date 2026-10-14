package monitor

import (
	"context"
	"fmt"
	"sync"

	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/internal/models"
)

type backendBoardReader interface {
	Boards(context.Context) ([]backendstore.Board, error)
	BoardDetail(context.Context, string, bool) (backendstore.BoardDetail, error)
}

type BackendBoardSource struct {
	ctx         context.Context
	reader      backendBoardReader
	guard       func(context.Context) error
	preferences *localMonitorPreferences
	mu          sync.Mutex
}

func NewBackendBoardSource(ctx context.Context, reader backendBoardReader, guard func(context.Context) error, preferences *localMonitorPreferences) *BackendBoardSource {
	return &BackendBoardSource{ctx: ctx, reader: reader, guard: guard, preferences: preferences}
}

func (s *BackendBoardSource) ListBoards() ([]models.Board, error) {
	if err := s.guard(s.ctx); err != nil {
		return nil, err
	}
	rows, err := s.reader.Boards(s.ctx)
	if err != nil {
		return nil, err
	}
	if err := s.guard(s.ctx); err != nil {
		return nil, err
	}
	out := make([]models.Board, 0, len(rows))
	for _, row := range rows {
		board, err := s.displayBoard(row)
		if err != nil {
			return nil, err
		}
		out = append(out, *board)
	}
	return out, nil
}

func (s *BackendBoardSource) LastViewedBoard() (*models.Board, error) {
	prefs, err := s.preferences.Load()
	if err != nil || prefs.LastBoardID == "" {
		return nil, err
	}
	boards, err := s.ListBoards()
	if err != nil {
		return nil, err
	}
	for i := range boards {
		if boards[i].ID == prefs.LastBoardID {
			return &boards[i], nil
		}
	}
	return nil, nil
}

func (s *BackendBoardSource) LoadBoard(id string, statuses map[models.Status]bool) BoardIssuesMsg {
	fail := func(err error) BoardIssuesMsg { return BoardIssuesMsg{BoardID: id, Error: err} }
	if err := s.guard(s.ctx); err != nil {
		return fail(err)
	}
	detail, err := s.reader.BoardDetail(s.ctx, id, true)
	if err != nil {
		return fail(err)
	}
	if err := s.guard(s.ctx); err != nil {
		return fail(err)
	}
	board, err := s.displayBoard(detail.Board)
	if err != nil {
		return fail(err)
	}
	issues := make([]models.BoardIssueView, 0, len(detail.Issues))
	for _, card := range detail.Issues {
		if len(statuses) > 0 && !statuses[card.Issue.Status] {
			continue
		}
		issues = append(issues, card.Model())
	}
	return BoardIssuesMsg{BoardID: id, Board: board, Issues: issues, ViewStore: &backendBoardView{source: s, observed: detail.Board}}
}

func (s *BackendBoardSource) displayBoard(row backendstore.Board) (*models.Board, error) {
	board := models.Board{ID: row.ID, Name: row.Name, Query: row.Query, IsBuiltin: row.IsBuiltin, ViewMode: row.ViewMode, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
	if s.preferences != nil {
		prefs, err := s.preferences.Load()
		if err != nil {
			return nil, err
		}
		if local := prefs.BoardViews[row.ID]; local != "" {
			board.ViewMode = local
		}
	}
	return &board, nil
}

type backendBoardView struct {
	source   *BackendBoardSource
	observed backendstore.Board
}

func (v *backendBoardView) SetViewMode(mode string) (*models.Board, BoardViewStore, error) {
	if mode != "backlog" && mode != "swimlanes" {
		return nil, nil, fmt.Errorf("invalid board view mode %q", mode)
	}
	if err := v.source.guard(v.source.ctx); err != nil {
		return nil, nil, err
	}
	v.source.mu.Lock()
	defer v.source.mu.Unlock()
	if err := v.source.preferences.Update(func(p *monitorPreferences) { p.BoardViews[v.observed.ID] = mode }); err != nil {
		return nil, nil, err
	}
	board, err := v.source.displayBoard(v.observed)
	if err != nil {
		return nil, nil, err
	}
	return board, v, nil
}

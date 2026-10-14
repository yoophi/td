package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"

	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/internal/models"
)

type backendBoardReader interface {
	Boards(context.Context) ([]backendstore.Board, error)
	BoardDetail(context.Context, string, bool) (backendstore.BoardDetail, error)
}

type backendBoardWriter interface {
	backendBoardReader
	RunIntent(context.Context, any, func() (backendstore.MutationRequest, error), any, func() error) error
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
	msg := BoardIssuesMsg{BoardID: id, Board: board, Issues: issues, ViewStore: &backendBoardView{source: s, observed: detail.Board}}
	if writer, ok := s.reader.(backendBoardWriter); ok {
		msg.MoveStore = &backendBoardMove{source: s, writer: writer, observed: detail}
	}
	return msg
}

type backendBoardMove struct {
	source   *BackendBoardSource
	writer   backendBoardWriter
	observed backendstore.BoardDetail
}

func (m *backendBoardMove) MoveBefore(id, before string, visible []string) error {
	anchor, err := backendMoveAnchor(m.observed.Issues, id, before, visible)
	if err != nil {
		return err
	}
	input := struct {
		Board, Revision, Issue, Before string
		Visible                        []string
	}{m.observed.Board.ID, m.observed.Board.Revision, id, anchor, slices.Clone(visible)}
	var reply struct {
		Board backendstore.Board `json:"board"`
	}
	return m.writer.RunIntent(m.source.ctx, input, func() (backendstore.MutationRequest, error) {
		request := backendstore.MutationRequest{}
		if err := m.source.guard(m.source.ctx); err != nil {
			return request, err
		}
		current, err := m.writer.BoardDetail(m.source.ctx, m.observed.Board.ID, true)
		if err != nil {
			return request, err
		}
		if err := m.source.guard(m.source.ctx); err != nil {
			return request, err
		}
		if current.Board.Revision != m.observed.Board.Revision || !sameBackendBoardCards(current.Issues, m.observed.Issues) {
			return request, fmt.Errorf("board changed since it was displayed; refresh before moving a card")
		}
		body, err := json.Marshal(map[string]any{"issue_id": id, "before_id": anchor, "include_closed": true})
		if err != nil {
			return request, err
		}
		return backendstore.MutationRequest{Method: "POST", Resource: "/boards/" + m.observed.Board.ID + "/move", Revision: m.observed.Board.Revision, Body: body}, nil
	}, &reply, func() error {
		if err := backendstore.ValidateBoard(reply.Board); err != nil {
			return err
		}
		if reply.Board.ID != m.observed.Board.ID {
			return fmt.Errorf("backend returned a different board")
		}
		return nil
	})
}

func sameBackendBoardCards(a, b []backendstore.BoardCard) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Issue.ID != b[i].Issue.ID || a[i].Issue.Revision != b[i].Issue.Revision || a[i].Position != b[i].Position || a[i].HasPosition != b[i].HasPosition {
			return false
		}
	}
	return true
}

func backendMoveAnchor(cards []backendstore.BoardCard, id, before string, visible []string) (string, error) {
	if id == before || len(visible) == 0 || !slices.Contains(visible, id) || before != "" && !slices.Contains(visible, before) {
		return "", fmt.Errorf("board changed: invalid moved card or anchor")
	}
	all := make([]string, 0, len(cards))
	for _, card := range cards {
		all = append(all, card.Issue.ID)
	}
	seen := map[string]bool{}
	last := -1
	for _, key := range visible {
		index := slices.Index(all, key)
		if seen[key] || index <= last {
			return "", fmt.Errorf("board changed: visible cards no longer match the observation")
		}
		seen[key] = true
		last = index
	}
	if before != "" {
		return before, nil
	}
	for next := last + 1; next < len(all); next++ {
		if all[next] != id {
			return all[next], nil
		}
	}
	return "", nil
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

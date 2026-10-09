package monitor

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
)

type BoardViewStore interface {
	SetViewMode(string) (*models.Board, BoardViewStore, error)
}
type githubBoardViewClient interface {
	SetBoardViewModeObserved(context.Context, *ghstore.BoardRecord, string, string) (*ghstore.BoardRecord, error)
	MaterializeBuiltinBoardObserved(context.Context, *ghstore.BoardRecord, string) (*ghstore.BoardRecord, error)
}
type githubBoardView struct {
	source   *GitHubBoardSource
	observed ghstore.BoardRecord
}

func (v *githubBoardView) SetViewMode(mode string) (*models.Board, BoardViewStore, error) {
	if mode != "swimlanes" && mode != "backlog" {
		return nil, nil, fmt.Errorf("invalid board view mode %q", mode)
	}
	c, err := v.source.open(v.source.ctx)
	if err != nil {
		return nil, nil, err
	}
	writer, ok := c.(githubBoardViewClient)
	if !ok {
		return nil, nil, fmt.Errorf("GitHub board view writer unavailable")
	}
	observed := v.observed
	created := false
	if observed.Number == 0 {
		b, err := writer.MaterializeBuiltinBoardObserved(v.source.ctx, &observed, v.source.actor)
		if err != nil {
			return nil, nil, err
		}
		observed = *b
		created = true
	}
	saved, err := writer.SetBoardViewModeObserved(v.source.ctx, &observed, mode, v.source.actor)
	if err != nil {
		if created {
			err = fmt.Errorf("builtin carrier was created, but view save failed; inspect GitHub before retrying: %w", err)
		}
		return nil, nil, err
	}
	return &saved.Board, &githubBoardView{source: v.source, observed: *saved}, nil
}

type BoardViewSavedMsg struct {
	SelectedID          string
	BoardID             string
	Request, Generation uint64
	Board               *models.Board
	Store               BoardViewStore
	Error               error
}

func (m Model) toggleRemoteBoardView() (Model, tea.Cmd) {
	if m.TaskListMode != TaskListModeBoard || m.BoardMode.Board == nil {
		return m, nil
	}
	if m.BoardViewPending || m.BoardMovePending || m.BoardVisitPending {
		m.StatusMessage = "Board view save is still running"
		return m, nil
	}
	store := m.BoardMode.ViewStore
	if store == nil {
		m.StatusMessage = "Board view observation unavailable; refresh before changing view"
		m.StatusIsError = true
		return m, nil
	}
	mode := "backlog"
	if m.BoardMode.ViewMode == BoardViewBacklog {
		mode = "swimlanes"
	}
	selected := ""
	if m.BoardMode.ViewMode == BoardViewBacklog {
		if i := m.BoardMode.Cursor; i >= 0 && i < len(m.BoardMode.Issues) {
			selected = m.BoardMode.Issues[i].Issue.ID
		}
	} else {
		if i := m.BoardMode.SwimlaneCursor; i >= 0 && i < len(m.BoardMode.SwimlaneRows) {
			selected = m.BoardMode.SwimlaneRows[i].Issue.ID
		}
	}
	id := m.BoardMode.Board.ID
	generation := m.BoardMode.ViewGeneration
	m.BoardViewRequest++
	request := m.BoardViewRequest
	m.BoardViewPending = true
	return m, func() tea.Msg {
		b, next, err := store.SetViewMode(mode)
		return BoardViewSavedMsg{SelectedID: selected, BoardID: id, Request: request, Generation: generation, Board: b, Store: next, Error: err}
	}
}

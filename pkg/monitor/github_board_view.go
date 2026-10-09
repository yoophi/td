package monitor

import (
	"fmt"

	tea "charm.land/bubbletea/v2"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
)

type BoardViewStore interface {
	SetViewMode(string) (*models.Board, BoardViewStore, error)
}
type githubBoardView struct {
	source   *GitHubBoardSource
	observed ghstore.BoardRecord
}

func (v *githubBoardView) SetViewMode(mode string) (*models.Board, BoardViewStore, error) {
	if mode != "swimlanes" && mode != "backlog" {
		return nil, nil, fmt.Errorf("invalid board view mode %q", mode)
	}
	if err := v.source.ctx.Err(); err != nil {
		return nil, nil, err
	}
	if err := v.source.preferences.Update(func(p *monitorPreferences) { p.BoardViews[v.observed.ID] = mode }); err != nil {
		return nil, nil, err
	}
	display, err := v.source.displayBoard(v.observed.Board)
	if err != nil {
		return nil, nil, err
	}
	return display, v, nil
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

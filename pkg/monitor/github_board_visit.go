package monitor

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
)

type BoardVisitStore interface{ MarkViewed() (*models.Board, error) }
type githubBoardVisitClient interface {
	MarkBoardViewedObserved(context.Context, *ghstore.BoardRecord, string) (*ghstore.BoardRecord, error)
	MaterializeBuiltinBoardObserved(context.Context, *ghstore.BoardRecord, string) (*ghstore.BoardRecord, error)
}

func (e *githubBoardEditor) MarkViewed() (*models.Board, error) {
	if strings.TrimSpace(e.source.actor) == "" {
		return nil, fmt.Errorf("board visit requires actual monitor session")
	}
	c, err := e.source.open(e.source.ctx)
	if err != nil {
		return nil, err
	}
	writer, ok := c.(githubBoardVisitClient)
	if !ok {
		return nil, fmt.Errorf("GitHub board visit writer unavailable")
	}
	observed := e.observed
	created := false
	if observed.Number == 0 {
		b, err := writer.MaterializeBuiltinBoardObserved(e.source.ctx, &observed, e.source.actor)
		if err != nil {
			return nil, err
		}
		observed = *b
		created = true
	}
	saved, err := writer.MarkBoardViewedObserved(e.source.ctx, &observed, e.source.actor)
	if err != nil {
		if created {
			err = fmt.Errorf("builtin carrier was created, but last-viewed save failed; inspect GitHub before retrying: %w", err)
		}
		return nil, err
	}
	return &saved.Board, nil
}

type BoardVisitedMsg struct {
	BoardID string
	Request uint64
	Board   *models.Board
	Error   error
}

func (m Model) recordRemoteBoardVisit(id string) (Model, tea.Cmd) {
	visit, ok := m.AllBoardEditors[id].(BoardVisitStore)
	if !ok {
		m.StatusMessage = "Board visit observation unavailable; refresh the picker before saving last viewed"
		m.StatusIsError = true
		return m, m.fetchBoardIssues(id)
	}
	m.BoardVisitRequest++
	request := m.BoardVisitRequest
	m.BoardVisitPending = true
	m.StatusMessage = "Saving last viewed board"
	m.StatusIsError = false
	return m, func() tea.Msg {
		b, err := visit.MarkViewed()
		return BoardVisitedMsg{BoardID: id, Request: request, Board: b, Error: err}
	}
}

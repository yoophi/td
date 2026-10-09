package monitor

import (
	tea "charm.land/bubbletea/v2"
	"github.com/marcus/td/internal/models"
)

type BoardVisitStore interface{ MarkViewed() (*models.Board, error) }

func (e *githubBoardEditor) MarkViewed() (*models.Board, error) {
	if err := e.source.ctx.Err(); err != nil {
		return nil, err
	}
	if err := e.source.preferences.Update(func(p *monitorPreferences) { p.LastBoardID = e.observed.ID }); err != nil {
		return nil, err
	}
	return e.source.displayBoard(e.observed.Board)
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
	m.StatusMessage = "Saving last viewed board on this device"
	m.StatusIsError = false
	return m, func() tea.Msg {
		b, err := visit.MarkViewed()
		return BoardVisitedMsg{BoardID: id, Request: request, Board: b, Error: err}
	}
}

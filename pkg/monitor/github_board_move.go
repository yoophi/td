package monitor

import (
	"context"
	"fmt"
	"slices"

	tea "charm.land/bubbletea/v2"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
)

type BoardMoveStore interface {
	MoveBefore(string, string, []string) error
}
type githubBoardMoveClient interface {
	Get(context.Context, string) (*ghstore.Record, error)
	MaterializeBuiltinBoardObserved(context.Context, *ghstore.BoardRecord, string) (*ghstore.BoardRecord, error)
	MoveBoardBeforeObserved(context.Context, *ghstore.BoardRecord, string, string, []models.Issue, string) (*ghstore.BoardRecord, error)
}
type githubBoardMove struct {
	source     *GitHubBoardSource
	observed   ghstore.BoardRecord
	candidates []models.Issue
}

func (m *githubBoardMove) MoveBefore(id, before string, visible []string) error {
	candidates := make([]models.Issue, 0, len(visible))
	seen := map[string]bool{}
	for _, key := range visible {
		if seen[key] {
			return fmt.Errorf("displayed board repeated task %s", key)
		}
		seen[key] = true
		index := slices.IndexFunc(m.candidates, func(i models.Issue) bool { return i.ID == key })
		if index < 0 {
			return fmt.Errorf("board changed: task %s is absent from the original observation", key)
		}
		candidates = append(candidates, m.candidates[index])
	}
	if !seen[id] || before != "" && !seen[before] || id == before {
		return fmt.Errorf("board changed: invalid moved task or anchor")
	}
	c, err := m.source.open(m.source.ctx)
	if err != nil {
		return err
	}
	writer, ok := c.(githubBoardMoveClient)
	if !ok {
		return fmt.Errorf("GitHub board movement writer unavailable")
	}
	for _, target := range []string{id, before} {
		if target != "" {
			if _, err := writer.Get(m.source.ctx, target); err != nil {
				return fmt.Errorf("verify move target before write: %w", err)
			}
		}
	}
	observed := m.observed
	created := false
	if observed.Number == 0 {
		b, err := writer.MaterializeBuiltinBoardObserved(m.source.ctx, &observed, m.source.actor)
		if err != nil {
			return err
		}
		observed = *b
		created = true
	}
	_, err = writer.MoveBoardBeforeObserved(m.source.ctx, &observed, id, before, candidates, m.source.actor)
	if err != nil && created {
		return fmt.Errorf("builtin carrier was created, but move failed; inspect GitHub before retrying: %w", err)
	}
	return err
}

type BoardMovedMsg struct {
	BoardID, IssueID string
	Request          uint64
	Error            error
}

func (m Model) moveRemoteBoard(direction int, edge string) (Model, tea.Cmd) {
	if m.TaskListMode != TaskListModeBoard || m.BoardMode.Board == nil {
		return m, nil
	}
	if m.BoardMovePending || m.BoardViewPending || m.BoardVisitPending {
		m.StatusMessage = "A board change is still running"
		return m, nil
	}
	store := m.BoardMode.MoveStore
	if store == nil {
		m.StatusMessage = "Board move observation unavailable; refresh first"
		m.StatusIsError = true
		return m, nil
	}
	ids := []string{}
	cursor := m.BoardMode.Cursor
	if m.BoardMode.ViewMode == BoardViewSwimlanes {
		cursor = m.BoardMode.SwimlaneCursor
		if cursor < 0 || cursor >= len(m.BoardMode.SwimlaneRows) {
			return m, nil
		}
		category := m.BoardMode.SwimlaneRows[cursor].Category
		selected := m.BoardMode.SwimlaneRows[cursor].Issue.ID
		for _, row := range m.BoardMode.SwimlaneRows {
			if row.Category == category {
				ids = append(ids, row.Issue.ID)
			}
		}
		cursor = slices.Index(ids, selected)
	} else {
		for _, v := range m.BoardMode.Issues {
			ids = append(ids, v.Issue.ID)
		}
	}
	if cursor < 0 || cursor >= len(ids) {
		return m, nil
	}
	target := cursor + direction
	switch edge {
	case "top":
		target = 0
	case "bottom":
		target = len(ids) - 1
	}
	if target < 0 || target >= len(ids) || target == cursor {
		return m, nil
	}
	selected := ids[cursor]
	order := slices.Clone(ids)
	order = slices.Delete(order, cursor, cursor+1)
	before := ""
	if target < len(order) {
		before = order[target]
	}
	id := m.BoardMode.Board.ID
	m.BoardMoveRequest++
	request := m.BoardMoveRequest
	m.BoardMovePending = true
	return m, func() tea.Msg {
		return BoardMovedMsg{BoardID: id, IssueID: selected, Request: request, Error: store.MoveBefore(selected, before, ids)}
	}
}

package monitor

import (
	"context"
	"fmt"
	"strings"

	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
)

// BoardEditorStore holds the original listing observation independently of
// later refreshes. Save/Delete must not replace it with a fresh observation.
type BoardEditorStore interface {
	Save(string, string) (*models.Board, error)
	Delete() error
}
type BoardEditorSource interface {
	ListBoardsForEditing() ([]models.Board, map[string]BoardEditorStore, error)
	CreateBoard(string, string) (*models.Board, error)
}
type githubBoardEditorClient interface {
	CreateBoard(context.Context, string, string, string) (*ghstore.BoardRecord, error)
	UpdateBoardObserved(context.Context, *ghstore.BoardRecord, ghstore.BoardChanges, string) (*ghstore.BoardRecord, error)
	DeleteBoardObserved(context.Context, *ghstore.BoardRecord, string, string) (*ghstore.BoardRecord, error)
}
type githubBoardEditor struct {
	source   *GitHubBoardSource
	observed ghstore.BoardRecord
}

func (s *GitHubBoardSource) ListBoardsForEditing() ([]models.Board, map[string]BoardEditorStore, error) {
	c, err := s.open(s.ctx)
	if err != nil {
		return nil, nil, err
	}
	records, err := c.ListBoards(s.ctx)
	if err != nil {
		return nil, nil, err
	}
	boards := make([]models.Board, 0, len(records))
	editors := map[string]BoardEditorStore{}
	for _, r := range records {
		board, err := s.displayBoard(r.Board)
		if err != nil {
			return nil, nil, err
		}
		boards = append(boards, *board)
		editors[r.ID] = &githubBoardEditor{source: s, observed: r}
	}
	return boards, editors, nil
}
func (s *GitHubBoardSource) editorClient() (githubBoardEditorClient, error) {
	if strings.TrimSpace(s.actor) == "" {
		return nil, fmt.Errorf("board editor requires actual monitor session")
	}
	c, err := s.open(s.ctx)
	if err != nil {
		return nil, err
	}
	writer, ok := c.(githubBoardEditorClient)
	if !ok {
		return nil, fmt.Errorf("GitHub board editor writer unavailable")
	}
	return writer, nil
}
func (s *GitHubBoardSource) CreateBoard(name, expression string) (*models.Board, error) {
	c, err := s.editorClient()
	if err != nil {
		return nil, err
	}
	b, err := c.CreateBoard(s.ctx, name, expression, s.actor)
	if err != nil {
		return nil, err
	}
	return s.displayBoard(b.Board)
}
func (e *githubBoardEditor) Save(name, expression string) (*models.Board, error) {
	c, err := e.source.editorClient()
	if err != nil {
		return nil, err
	}
	// Keep the original observation for each attempt; never refresh it here.
	observed := e.observed
	b, err := c.UpdateBoardObserved(e.source.ctx, &observed, ghstore.BoardChanges{Name: &name, Query: &expression}, e.source.actor)
	if err != nil {
		return nil, err
	}
	return e.source.displayBoard(b.Board)
}
func (e *githubBoardEditor) Delete() error {
	c, err := e.source.editorClient()
	if err != nil {
		return err
	}
	observed := e.observed
	_, err = c.DeleteBoardObserved(e.source.ctx, &observed, e.source.actor, "")
	return err
}

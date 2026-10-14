package monitor

import (
	"encoding/json"
	"fmt"

	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/internal/models"
)

type backendBoardEditor struct {
	source   *BackendBoardSource
	writer   backendBoardWriter
	observed backendstore.Board
}

func (s *BackendBoardSource) ListBoardsForEditing() ([]models.Board, map[string]BoardEditorStore, error) {
	if err := s.guard(s.ctx); err != nil {
		return nil, nil, err
	}
	rows, err := s.reader.Boards(s.ctx)
	if err != nil {
		return nil, nil, err
	}
	if err := s.guard(s.ctx); err != nil {
		return nil, nil, err
	}
	boards := make([]models.Board, 0, len(rows))
	editors := map[string]BoardEditorStore{}
	writer, writable := s.reader.(backendBoardWriter)
	for _, row := range rows {
		board, err := s.displayBoard(row)
		if err != nil {
			return nil, nil, err
		}
		boards = append(boards, *board)
		if writable {
			editors[row.ID] = &backendBoardEditor{source: s, writer: writer, observed: row}
		}
	}
	return boards, editors, nil
}

func (s *BackendBoardSource) CreateBoard(name, expression string) (*models.Board, error) {
	writer, ok := s.reader.(backendBoardWriter)
	if !ok {
		return nil, fmt.Errorf("backend board writer unavailable")
	}
	var out struct {
		Board backendstore.Board `json:"board"`
	}
	input := struct{ Kind, Name, Query string }{"board.create", name, expression}
	err := writer.RunIntent(s.ctx, input, func() (backendstore.MutationRequest, error) {
		if err := s.guard(s.ctx); err != nil {
			return backendstore.MutationRequest{}, err
		}
		body, err := json.Marshal(map[string]string{"name": name, "query": expression})
		return backendstore.MutationRequest{Method: "POST", Resource: "/boards", Body: body}, err
	}, &out, func() error { return backendstore.ValidateBoard(out.Board) })
	if err != nil {
		return nil, err
	}
	if err := s.guard(s.ctx); err != nil {
		return nil, err
	}
	return s.displayBoard(out.Board)
}

func (e *backendBoardEditor) Save(name, expression string) (*models.Board, error) {
	var out struct {
		Board backendstore.Board `json:"board"`
	}
	input := struct{ Kind, Board, Revision, Name, Query string }{"board.edit", e.observed.ID, e.observed.Revision, name, expression}
	err := e.writer.RunIntent(e.source.ctx, input, func() (backendstore.MutationRequest, error) {
		request, err := e.observedRequest("PATCH", map[string]string{"name": name, "query": expression})
		return request, err
	}, &out, func() error {
		if err := backendstore.ValidateBoard(out.Board); err != nil {
			return err
		}
		if out.Board.ID != e.observed.ID {
			return fmt.Errorf("backend returned a different board")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return e.source.displayBoard(out.Board)
}

func (e *backendBoardEditor) Delete() error {
	if e.observed.IsBuiltin {
		return fmt.Errorf("builtin board cannot be deleted")
	}
	var out struct {
		Deleted bool `json:"deleted"`
	}
	input := struct{ Kind, Board, Revision string }{"board.delete", e.observed.ID, e.observed.Revision}
	return e.writer.RunIntent(e.source.ctx, input, func() (backendstore.MutationRequest, error) {
		return e.observedRequest("DELETE", nil)
	}, &out, func() error {
		if !out.Deleted {
			return fmt.Errorf("backend did not confirm board deletion")
		}
		return nil
	})
}

func (e *backendBoardEditor) observedRequest(method string, payload any) (backendstore.MutationRequest, error) {
	request := backendstore.MutationRequest{}
	if err := e.source.guard(e.source.ctx); err != nil {
		return request, err
	}
	rows, err := e.writer.Boards(e.source.ctx)
	if err != nil {
		return request, err
	}
	if err := e.source.guard(e.source.ctx); err != nil {
		return request, err
	}
	found := false
	for _, row := range rows {
		if row.ID == e.observed.ID {
			found = true
			if row.Revision != e.observed.Revision {
				return request, fmt.Errorf("board changed since it was displayed; refresh before editing")
			}
		}
	}
	if !found {
		return request, fmt.Errorf("board no longer exists")
	}
	request = backendstore.MutationRequest{Method: method, Resource: "/boards/" + e.observed.ID, Revision: e.observed.Revision}
	if payload != nil {
		request.Body, err = json.Marshal(payload)
	}
	return request, err
}

func (e *backendBoardEditor) MarkViewed() (*models.Board, error) {
	if err := e.source.guard(e.source.ctx); err != nil {
		return nil, err
	}
	if err := e.source.preferences.Update(func(p *monitorPreferences) { p.LastBoardID = e.observed.ID }); err != nil {
		return nil, err
	}
	return e.source.displayBoard(e.observed)
}

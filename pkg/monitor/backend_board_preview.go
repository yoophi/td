package monitor

import (
	"context"
	"fmt"

	"github.com/marcus/td/internal/backendstore"
)

type backendBoardPreviewReader interface {
	ReadConsistent(context.Context, func() error) error
	QueryIDs(context.Context, string) ([]string, error)
	List(context.Context, bool) ([]backendstore.Issue, error)
}

func (s *BackendBoardSource) PreviewQuery(expression string) BoardEditorQueryPreviewMsg {
	s.CancelPreview()
	result := BoardEditorQueryPreviewMsg{Query: expression, Titles: []string{}}
	if expression == "" {
		return result
	}
	fail := func(err error) BoardEditorQueryPreviewMsg { result.Error = err; return result }
	if s.guard == nil {
		return fail(fmt.Errorf("backend board context unavailable"))
	}
	reader, ok := s.reader.(backendBoardPreviewReader)
	if !ok {
		return fail(fmt.Errorf("backend board preview reader unavailable"))
	}
	ctx, cancel := s.previewContext()
	defer cancel()
	if err := s.guard(ctx); err != nil {
		return fail(err)
	}
	var ids []string
	var titles map[string]string
	err := reader.ReadConsistent(ctx, func() error {
		var err error
		ids, err = reader.QueryIDs(ctx, expression)
		if err != nil {
			return err
		}
		rows, err := reader.List(ctx, true)
		if err != nil {
			return err
		}
		titles = make(map[string]string, len(rows))
		for _, row := range rows {
			titles[row.ID] = row.Title
		}
		for _, id := range ids {
			if _, ok := titles[id]; !ok {
				return fmt.Errorf("board query issue %s unavailable in snapshot", id)
			}
		}
		return nil
	})
	if err != nil {
		return fail(err)
	}
	if err := s.guard(ctx); err != nil {
		return fail(err)
	}
	result.Count = len(ids)
	if result.Count > 5 {
		result.Count = -1
	}
	for i, id := range ids {
		if i >= 5 {
			break
		}
		result.Titles = append(result.Titles, titles[id])
	}
	return result
}

func (s *BackendBoardSource) CancelPreview() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.previewCancel != nil {
		s.previewCancel()
		s.previewCancel = nil
	}
}

func (s *BackendBoardSource) previewContext() (context.Context, context.CancelFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.previewCancel != nil {
		s.previewCancel()
	}
	ctx, cancel := context.WithCancel(s.ctx)
	s.previewCancel = cancel
	return ctx, cancel
}

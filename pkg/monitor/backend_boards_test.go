package monitor

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/internal/models"
)

type backendBoardFixture struct {
	board backendstore.Board
	cards []backendstore.BoardCard
}

func (f backendBoardFixture) Boards(context.Context) ([]backendstore.Board, error) {
	return []backendstore.Board{f.board}, nil
}
func (f backendBoardFixture) BoardDetail(_ context.Context, id string, _ bool) (backendstore.BoardDetail, error) {
	return backendstore.BoardDetail{Board: f.board, Issues: f.cards}, nil
}

func TestBackendBoardReadAndLocalViewPreference(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	board := backendstore.Board{ID: "brd-1", Name: "Work", ViewMode: "backlog", CreatedAt: now, UpdatedAt: now, Revision: "r-1"}
	fixture := backendBoardFixture{board: board, cards: []backendstore.BoardCard{{BoardID: board.ID, Category: "ready", Issue: backendstore.Issue{Issue: models.Issue{ID: "td-0123456789abcdef", Status: models.StatusOpen}, Revision: "r-1"}}}}
	prefs := &localMonitorPreferences{ctx: ctx, path: filepath.Join(t.TempDir(), "monitor.json")}
	source := NewBackendBoardSource(ctx, fixture, func(context.Context) error { return nil }, prefs)
	msg := source.LoadBoard(board.ID, map[models.Status]bool{models.StatusOpen: true})
	if msg.Error != nil || msg.Board == nil || len(msg.Issues) != 1 || msg.Issues[0].Category != "ready" {
		t.Fatalf("board snapshot was not rendered: %+v", msg)
	}
	changed, _, err := msg.ViewStore.SetViewMode("swimlanes")
	if err != nil || changed.ViewMode != "swimlanes" {
		t.Fatalf("local board view not saved: %+v, %v", changed, err)
	}
	listed, err := source.ListBoards()
	if err != nil || len(listed) != 1 || listed[0].ViewMode != "swimlanes" || listed[0].LastViewedAt != nil {
		t.Fatalf("local preference not applied: %+v, %v", listed, err)
	}
	if fixture.board.ViewMode != "backlog" {
		t.Fatal("shared board was changed")
	}
}

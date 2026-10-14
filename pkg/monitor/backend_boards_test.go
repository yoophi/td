package monitor

import (
	"context"
	"encoding/json"
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

type backendBoardWriterFixture struct {
	backendBoardFixture
	request backendstore.MutationRequest
	stale   bool
	writes  int
}

func (f *backendBoardWriterFixture) BoardDetail(ctx context.Context, id string, closed bool) (backendstore.BoardDetail, error) {
	detail, err := f.backendBoardFixture.BoardDetail(ctx, id, closed)
	if f.stale {
		detail.Board.Revision = "r-2"
	}
	return detail, err
}
func (f *backendBoardWriterFixture) RunIntent(_ context.Context, _ any, prepare func() (backendstore.MutationRequest, error), out any, verify func() error) error {
	var err error
	f.request, err = prepare()
	if err != nil {
		return err
	}
	f.writes++
	response := map[string]any{"board": f.board}
	if f.request.Method == "DELETE" {
		response = map[string]any{"deleted": true}
	}
	data, _ := json.Marshal(response)
	if err = json.Unmarshal(data, out); err != nil {
		return err
	}
	return verify()
}

func TestBackendBoardEditorUsesObservedRevisionAndLocalVisit(t *testing.T) {
	now := time.Now()
	fixture := &backendBoardWriterFixture{backendBoardFixture: backendBoardFixture{board: backendstore.Board{ID: "brd-1", Name: "Work", Query: "status = open", Revision: "r-1", CreatedAt: now, UpdatedAt: now}}}
	prefs := &localMonitorPreferences{ctx: context.Background(), path: filepath.Join(t.TempDir(), "prefs.json")}
	source := NewBackendBoardSource(context.Background(), fixture, func(context.Context) error { return nil }, prefs)
	boards, editors, err := source.ListBoardsForEditing()
	if err != nil || len(boards) != 1 || editors["brd-1"] == nil {
		t.Fatalf("board editor unavailable: %+v, %v", editors, err)
	}
	visit := editors["brd-1"].(BoardVisitStore)
	if _, err := visit.MarkViewed(); err != nil || fixture.writes != 0 {
		t.Fatalf("personal board visit wrote remotely: %v", err)
	}
	if last, err := source.LastViewedBoard(); err != nil || last == nil || last.ID != "brd-1" {
		t.Fatalf("local last viewed board missing: %+v, %v", last, err)
	}
	if _, err := source.CreateBoard("Fresh", "type = task"); err != nil || fixture.request.Method != "POST" || fixture.request.Resource != "/boards" {
		t.Fatalf("board creation failed: %+v, %v", fixture.request, err)
	}
	if _, err := editors["brd-1"].Save("Renamed", "type = task"); err != nil || fixture.request.Method != "PATCH" || fixture.request.Revision != "r-1" {
		t.Fatalf("observed board edit failed: %+v, %v", fixture.request, err)
	}
	fixture.stale = true
	fixture.board.Revision = "r-2"
	if err := editors["brd-1"].Delete(); err == nil {
		t.Fatal("stale board editor deleted board")
	}
	fixture.stale = false
	fixture.board.Revision = "r-1"
	if err := editors["brd-1"].Delete(); err != nil || fixture.request.Method != "DELETE" || fixture.request.Revision != "r-1" {
		t.Fatalf("observed board delete failed: %+v, %v", fixture.request, err)
	}
}

func (f backendBoardFixture) Boards(context.Context) ([]backendstore.Board, error) {
	return []backendstore.Board{f.board}, nil
}

func TestBackendBoardMovePreservesHiddenCardAnchorAndRejectsStaleBoard(t *testing.T) {
	now := time.Now()
	fixture := &backendBoardWriterFixture{backendBoardFixture: backendBoardFixture{
		board: backendstore.Board{ID: "brd-1", Name: "Work", ViewMode: "backlog", Revision: "r-1", CreatedAt: now, UpdatedAt: now},
		cards: []backendstore.BoardCard{
			{BoardID: "brd-1", Issue: backendstore.Issue{Issue: models.Issue{ID: "td-aaaaaaaaaaaaaaaa"}, Revision: "r-1"}},
			{BoardID: "brd-1", Issue: backendstore.Issue{Issue: models.Issue{ID: "td-bbbbbbbbbbbbbbbb"}, Revision: "r-1"}},
			{BoardID: "brd-1", Issue: backendstore.Issue{Issue: models.Issue{ID: "td-cccccccccccccccc"}, Revision: "r-1"}},
		},
	}}
	source := NewBackendBoardSource(context.Background(), fixture, func(context.Context) error { return nil }, &localMonitorPreferences{ctx: context.Background(), path: filepath.Join(t.TempDir(), "prefs.json")})
	msg := source.LoadBoard("brd-1", nil)
	if msg.Error != nil || msg.MoveStore == nil {
		t.Fatalf("move handle unavailable: %+v", msg)
	}
	if err := msg.MoveStore.MoveBefore("td-aaaaaaaaaaaaaaaa", "", []string{"td-aaaaaaaaaaaaaaaa", "td-bbbbbbbbbbbbbbbb"}); err != nil {
		t.Fatal(err)
	}
	var body struct {
		BeforeID string `json:"before_id"`
	}
	if err := json.Unmarshal(fixture.request.Body, &body); err != nil || body.BeforeID != "td-cccccccccccccccc" || fixture.request.Revision != "r-1" || fixture.request.Resource != "/boards/brd-1/move" {
		t.Fatalf("hidden card anchor was lost: %+v, %v", fixture.request, err)
	}
	fixture.stale = true
	if err := msg.MoveStore.MoveBefore("td-aaaaaaaaaaaaaaaa", "td-bbbbbbbbbbbbbbbb", []string{"td-aaaaaaaaaaaaaaaa", "td-bbbbbbbbbbbbbbbb"}); err == nil {
		t.Fatal("stale board move accepted")
	}
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

package monitor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
)

type visitBoardFixture struct {
	editableBoardFixture
	creates int
}

func (f *visitBoardFixture) MarkBoardViewedObserved(_ context.Context, b *ghstore.BoardRecord, actor string) (*ghstore.BoardRecord, error) {
	f.observedName = b.Name
	f.actor = actor
	if b.Name != f.boards[0].Name {
		return nil, &ghstore.ConflictError{ID: b.ID}
	}
	f.writes++
	if f.failure != nil {
		return nil, f.failure
	}
	saved := *b
	now := time.Now().UTC()
	saved.LastViewedAt = &now
	saved.Details.LastViewedAt = &now
	f.boards[0] = saved
	return &saved, nil
}
func (f *visitBoardFixture) MaterializeBuiltinBoardObserved(_ context.Context, b *ghstore.BoardRecord, actor string) (*ghstore.BoardRecord, error) {
	f.creates++
	saved := *b
	saved.Number = 9
	f.boards[0] = saved
	return &saved, nil
}
func TestGitHubBoardVisitFrozenObservationAndPartialCreation(t *testing.T) {
	f := &visitBoardFixture{editableBoardFixture: editableBoardFixture{boardSourceFixture: boardSourceFixture{boards: []ghstore.BoardRecord{{Board: models.Board{ID: "bd-gh-1", Name: "Original"}, Number: 1}}}}}
	source := &GitHubBoardSource{ctx: context.Background(), actor: "actual-monitor", open: func(context.Context) (githubBoardClient, error) { return f, nil }}
	_, handles, err := source.ListBoardsForEditing()
	if err != nil {
		t.Fatal(err)
	}
	visit := handles["bd-gh-1"].(BoardVisitStore)
	f.boards[0].Name = "Peer"
	var conflict *ghstore.ConflictError
	if _, err = visit.MarkViewed(); !errors.As(err, &conflict) || f.writes != 0 || f.observedName != "Original" {
		t.Fatalf("stale visit %v", err)
	}
	_, handles, err = source.ListBoardsForEditing()
	if err != nil {
		t.Fatal(err)
	}
	saved, err := handles["bd-gh-1"].(BoardVisitStore).MarkViewed()
	if err != nil || saved.LastViewedAt == nil || f.actor != "actual-monitor" || f.writes != 1 {
		t.Fatalf("%+v %v", saved, err)
	}
	f.boards[0].ID = "bd-all-issues"
	f.boards[0].IsBuiltin = true
	f.boards[0].Number = 0
	_, handles, err = source.ListBoardsForEditing()
	if err != nil {
		t.Fatal(err)
	}
	f.failure = errors.New("permission denied")
	_, err = handles["bd-all-issues"].(BoardVisitStore).MarkViewed()
	if err == nil || f.creates != 1 || !strings.Contains(err.Error(), "carrier was created") || f.writes != 2 {
		t.Fatalf("partial %v", err)
	}
}

func TestBoardSelectionRecordsVisitBeforeLoadingCardsWithoutSQLite(t *testing.T) {
	f := &visitBoardFixture{editableBoardFixture: editableBoardFixture{boardSourceFixture: boardSourceFixture{boards: []ghstore.BoardRecord{{Board: models.Board{ID: "bd-gh-1", Name: "Board", ViewMode: "backlog"}, Number: 1, Details: ghstore.BoardDetails{Version: 1, ViewMode: "backlog"}}}}}}
	// Override the editor fixture's GetBoard panic through a separate read client.
	source := &GitHubBoardSource{ctx: context.Background(), actor: "actual-monitor", baseDir: t.TempDir(), open: func(context.Context) (githubBoardClient, error) { return &visitReadableFixture{f}, nil }}
	msg := Model{BoardSource: source}.fetchBoards()().(BoardsDataMsg)
	m := Model{BoardSource: source, BoardPickerOpen: true, AllBoards: msg.Boards, AllBoardEditors: msg.Editors}
	m.BoardMode.Issues = []models.BoardIssueView{{Issue: models.Issue{ID: "old-board-task"}}}
	m, cmd := m.selectBoard()
	if cmd == nil || !m.BoardVisitPending || len(m.BoardMode.Issues) != 0 {
		t.Fatal("visit not queued or old cards retained")
	}
	m.BoardPickerOpen = true
	_, duplicate := m.selectBoard()
	if duplicate != nil {
		t.Fatal("duplicate selection write started")
	}
	visited := cmd().(BoardVisitedMsg)
	if visited.Error != nil || f.writes != 1 || f.actor != "actual-monitor" {
		t.Fatalf("%+v", visited)
	}
	result, load := m.Update(visited)
	m = result.(Model)
	if m.BoardVisitPending || load == nil || m.BoardMode.Board.LastViewedAt == nil {
		t.Fatal("saved visit did not load cards")
	}
	cards := load().(BoardIssuesMsg)
	if cards.Error != nil {
		t.Fatal(cards.Error)
	}
	// A failed visit is visible but does not prevent reading the selected board.
	m.BoardPickerOpen = true
	m.AllBoards = msg.Boards
	m.AllBoardEditors = msg.Editors
	f.failure = errors.New("write outcome unknown")
	m, cmd = m.selectBoard()
	visited = cmd().(BoardVisitedMsg)
	result, load = m.Update(visited)
	m = result.(Model)
	if !m.StatusIsError || m.BoardVisitPending || load == nil {
		t.Fatal("visit failure hidden")
	}
	result, ignored := m.Update(visited)
	if ignored != nil || !result.(Model).StatusIsError {
		t.Fatal("duplicate reply changed state")
	}
}

type visitReadableFixture struct{ *visitBoardFixture }

func (f *visitReadableFixture) GetBoard(context.Context, string) (*ghstore.BoardRecord, error) {
	b := f.boards[0]
	return &b, nil
}

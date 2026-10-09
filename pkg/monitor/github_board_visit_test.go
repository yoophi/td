package monitor

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
)

func TestGitHubBoardVisitIsDeviceLocalAndNeverMaterializesBuiltin(t *testing.T) {
	prefs := testMonitorPreferences(t)
	source := &GitHubBoardSource{ctx: context.Background(), preferences: prefs, actor: "actual", open: func(context.Context) (githubBoardClient, error) { t.Fatal("visit contacted GitHub"); return nil, nil }}
	board := ghstore.BoardRecord{Board: models.Board{ID: "bd-all-issues", Name: "All Issues", IsBuiltin: true}, Number: 0}
	e := &githubBoardEditor{source: source, observed: board}
	saved, err := e.MarkViewed()
	if err != nil || saved.ID != board.ID || saved.LastViewedAt != nil {
		t.Fatal(saved, err)
	}
	p, err := prefs.Load()
	if err != nil || p.LastBoardID != board.ID {
		t.Fatal(p, err)
	}
	// A peer rename of shared configuration does not invalidate a local visit.
	e.observed.Name = "Peer name"
	if _, err := e.MarkViewed(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(prefs.path)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	source.ctx = cancelled
	if _, err := e.MarkViewed(); err == nil {
		t.Fatal("cancelled local save succeeded")
	}
	after, _ := os.ReadFile(prefs.path)
	if string(after) != string(data) {
		t.Fatal("cancelled save changed preferences")
	}
}

func TestBoardSelectionRecordsLocalVisitBeforeLoadingCardsWithoutSQLite(t *testing.T) {
	prefs := testMonitorPreferences(t)
	now := time.Now()
	f := &boardSourceFixture{boards: []ghstore.BoardRecord{{Board: models.Board{ID: "bd-gh-1", Name: "Board", ViewMode: "backlog", LastViewedAt: &now}, Number: 1, Details: ghstore.BoardDetails{Version: 1, ViewMode: "backlog"}}}}
	source := &GitHubBoardSource{ctx: context.Background(), preferences: prefs, actor: "actual", baseDir: t.TempDir(), open: func(context.Context) (githubBoardClient, error) { return f, nil }}
	if last, err := source.LastViewedBoard(); err != nil || last != nil {
		t.Fatal("imported peer last-view clock", last, err)
	}
	msg := Model{BoardSource: source}.fetchBoards()().(BoardsDataMsg)
	m := Model{BoardSource: source, BoardPickerOpen: true, AllBoards: msg.Boards, AllBoardEditors: msg.Editors}
	m.BoardMode.Issues = []models.BoardIssueView{{Issue: models.Issue{ID: "old-task"}}}
	m, cmd := m.selectBoard()
	if cmd == nil || !m.BoardVisitPending || len(m.BoardMode.Issues) != 0 {
		t.Fatal("local visit not queued or old rows retained")
	}
	m.BoardPickerOpen = true
	if _, duplicate := m.selectBoard(); duplicate != nil {
		t.Fatal("duplicate visit")
	}
	visited := cmd().(BoardVisitedMsg)
	if visited.Error != nil || f.boards[0].LastViewedAt != &now {
		t.Fatal(visited, "shared clock changed")
	}
	result, load := m.Update(visited)
	m = result.(Model)
	if m.BoardVisitPending || load == nil || m.BoardMode.Board.LastViewedAt != nil {
		t.Fatal("local save not applied")
	}
	if cards := load().(BoardIssuesMsg); cards.Error != nil {
		t.Fatal(cards.Error)
	}
	last, err := source.LastViewedBoard()
	if err != nil || last == nil || last.ID != "bd-gh-1" {
		t.Fatal(last, err)
	}
	// A local persistence error is shown, and no duplicate completion can hide it.
	m.BoardPickerOpen = true
	m.AllBoards = msg.Boards
	m.AllBoardEditors = msg.Editors
	if err := os.WriteFile(prefs.path, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	m, cmd = m.selectBoard()
	visited = cmd().(BoardVisitedMsg)
	result, load = m.Update(visited)
	m = result.(Model)
	if !m.StatusIsError || m.BoardVisitPending || load == nil {
		t.Fatal("preference failure hidden")
	}
	result, ignored := m.Update(visited)
	if ignored != nil || !result.(Model).StatusIsError {
		t.Fatal("duplicate result changed state")
	}
}

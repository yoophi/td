package monitor

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
)

type editableBoardFixture struct {
	boardSourceFixture
	actor        string
	observedName string
	writes       int
	failure      error
}

func TestGitHubBoardEditorPendingAndUncertainCreate(t *testing.T) {
	f := &editableBoardFixture{failure: errors.New("write outcome unknown")}
	source := &GitHubBoardSource{ctx: context.Background(), actor: "actual", open: func(context.Context) (githubBoardClient, error) { return f, nil }}
	m := Model{BoardSource: source}
	m, _ = m.openBoardEditorCreate()
	m.BoardEditorNameInput.SetValue("New")
	m, cmd := m.executeBoardEditorSave()
	if cmd == nil || !m.BoardEditorPending {
		t.Fatal("create did not become pending")
	}
	if _, duplicate := m.executeBoardEditorSave(); duplicate != nil {
		t.Fatal("duplicate creation queued")
	}
	if _, duplicate := m.executeBoardEditorDelete(); duplicate != nil {
		t.Fatal("delete queued during creation")
	}
	result, _ := m.Update(cmd())
	m = result.(Model)
	if !m.BoardEditorOpen || m.BoardEditorPending || !m.StatusIsError || m.BoardEditorNameInput.Value() != "New" || f.writes != 1 {
		t.Fatal("uncertain create discarded draft or concealed outcome")
	}
	m, cmd = m.executeBoardEditorSave()
	if cmd != nil || !strings.Contains(m.StatusMessage, "inspect GitHub") || f.writes != 1 {
		t.Fatal("uncertain creation retried")
	}
}

func TestGitHubBoardEditorCompletionDoesNotCloseNewDraft(t *testing.T) {
	for _, deletion := range []bool{false, true} {
		t.Run(map[bool]string{false: "save", true: "delete"}[deletion], func(t *testing.T) {
			f := &editableBoardFixture{boardSourceFixture: boardSourceFixture{boards: []ghstore.BoardRecord{{Board: models.Board{ID: "bd-gh-1", Name: "Original"}}}}}
			source := &GitHubBoardSource{ctx: context.Background(), actor: "actual", open: func(context.Context) (githubBoardClient, error) { return f, nil }}
			m := Model{BoardSource: source, BoardPickerOpen: true}
			listed := m.fetchBoards()().(BoardsDataMsg)
			m.AllBoards, m.AllBoardEditors = listed.Boards, listed.Editors
			m, _ = m.openBoardEditor()
			m.BoardEditorNameInput.SetValue("Saved")
			var cmd tea.Cmd
			// Keep the real result command until another draft has opened.
			if deletion {
				next, write := m.executeBoardEditorDelete()
				m = next
				cmd = write
			} else {
				next, write := m.executeBoardEditorSave()
				m = next
				cmd = write
			}
			if _, duplicate := m.executeBoardEditorSave(); duplicate != nil {
				t.Fatal("duplicate save queued")
			}
			if _, duplicate := m.executeBoardEditorDelete(); duplicate != nil {
				t.Fatal("duplicate delete queued")
			}
			m.closeBoardEditorModal()
			m, _ = m.openBoardEditorCreate()
			m.BoardEditorNameInput.SetValue("Fresh draft")
			oldRequest := m.BoardEditorRequest
			stale, _ := m.Update(BoardEditorDeleteResultMsg{Request: oldRequest + 1, BoardID: "bd-gh-1"})
			if !stale.(Model).BoardEditorPending || !stale.(Model).BoardEditorOpen {
				t.Fatal("unmatched result altered editor")
			}
			result, _ := m.Update(cmd())
			m = result.(Model)
			if !m.BoardEditorOpen || m.BoardEditorPending || m.BoardEditorNameInput.Value() != "Fresh draft" || f.writes != 1 {
				t.Fatal("old completion closed new draft")
			}
		})
	}
}

func (f *editableBoardFixture) GetBoard(context.Context, string) (*ghstore.BoardRecord, error) {
	panic("editor replaced original observation")
}
func (f *editableBoardFixture) CreateBoard(_ context.Context, name, expression, actor string) (*ghstore.BoardRecord, error) {
	f.writes++
	f.actor = actor
	if f.failure != nil {
		return nil, f.failure
	}
	b := ghstore.BoardRecord{Board: models.Board{ID: "bd-gh-2", Name: name, Query: expression}}
	return &b, nil
}
func (f *editableBoardFixture) UpdateBoardObserved(_ context.Context, b *ghstore.BoardRecord, ch ghstore.BoardChanges, actor string) (*ghstore.BoardRecord, error) {
	f.observedName = b.Name
	f.actor = actor
	if b.Name != f.boards[0].Name {
		return nil, &ghstore.ConflictError{ID: b.ID}
	}
	f.writes++
	if f.failure != nil {
		return nil, f.failure
	}
	result := *b
	if ch.Name != nil {
		result.Name = *ch.Name
	}
	if ch.Query != nil {
		result.Query = *ch.Query
	}
	f.boards[0] = result
	return &result, nil
}
func (f *editableBoardFixture) DeleteBoardObserved(_ context.Context, b *ghstore.BoardRecord, actor, reason string) (*ghstore.BoardRecord, error) {
	f.observedName = b.Name
	f.actor = actor
	if b.Name != f.boards[0].Name {
		return nil, &ghstore.ConflictError{ID: b.ID}
	}
	f.writes++
	if f.failure != nil {
		return nil, f.failure
	}
	return b, nil
}
func TestGitHubBoardEditorFrozenObservationAndActor(t *testing.T) {
	f := &editableBoardFixture{boardSourceFixture: boardSourceFixture{boards: []ghstore.BoardRecord{{Board: models.Board{ID: "bd-gh-1", Name: "Original"}, Number: 1}}}}
	opens := 0
	source := &GitHubBoardSource{ctx: context.Background(), actor: "actual-monitor", open: func(context.Context) (githubBoardClient, error) { opens++; return f, nil }}
	boards, editors, err := source.ListBoardsForEditing()
	if err != nil {
		t.Fatal(err)
	}
	if boards[0].Name != "Original" {
		t.Fatal(boards)
	}
	old := editors["bd-gh-1"]
	f.boards[0].Name = "Peer change"
	_, fresh, err := source.ListBoardsForEditing()
	if err != nil {
		t.Fatal(err)
	}
	var conflict *ghstore.ConflictError
	if _, err = old.Save("Draft", "status = open"); !errors.As(err, &conflict) || f.writes != 0 || f.observedName != "Original" {
		t.Fatalf("frozen save %v", err)
	}
	if err = old.Delete(); !errors.As(err, &conflict) || f.writes != 0 {
		t.Fatalf("frozen delete %v", err)
	}
	saved, err := fresh["bd-gh-1"].Save("Saved", "status = open")
	if err != nil || saved.Name != "Saved" || f.actor != "actual-monitor" || f.writes != 1 {
		t.Fatalf("%+v %v", saved, err)
	}
	if opens != 5 {
		t.Fatalf("each operation must revalidate store: opens=%d", opens)
	}
	f.failure = errors.New("write outcome unknown; inspect before retrying")
	_, editors, err = source.ListBoardsForEditing()
	if err != nil {
		t.Fatal(err)
	}
	if err = editors["bd-gh-1"].Delete(); err == nil {
		t.Fatal("uncertain deletion hidden")
	}
	if f.writes != 2 {
		t.Fatal("write retried")
	}
	f.failure = nil
	created, err := source.CreateBoard("New", "status = open")
	if err != nil || created.Name != "New" || f.actor != "actual-monitor" {
		t.Fatalf("%+v %v", created, err)
	}
}

func TestBoardEditorModelUsesFrozenStoreWithoutSQLite(t *testing.T) {
	f := &editableBoardFixture{boardSourceFixture: boardSourceFixture{boards: []ghstore.BoardRecord{{Board: models.Board{ID: "bd-gh-1", Name: "Original"}, Number: 1}}}}
	source := &GitHubBoardSource{ctx: context.Background(), actor: "actual-monitor", open: func(context.Context) (githubBoardClient, error) { return f, nil }}
	m := Model{BoardSource: source, BoardPickerOpen: true}
	msg := m.fetchBoards()().(BoardsDataMsg)
	m.AllBoards = msg.Boards
	m.AllBoardEditors = msg.Editors
	m, _ = m.openBoardEditor()
	m.BoardEditorNameInput.SetValue("Draft")
	m.BoardEditorQueryInput.SetValue("status = open")
	f.boards[0].Name = "Peer change"
	refreshed := m.fetchBoards()().(BoardsDataMsg)
	m.AllBoards = refreshed.Boards
	m.AllBoardEditors = refreshed.Editors
	m, cmd := m.executeBoardEditorSave()
	if cmd == nil {
		t.Fatal("save command missing")
	}
	saved := cmd().(BoardEditorSaveResultMsg)
	var conflict *ghstore.ConflictError
	if !errors.As(saved.Error, &conflict) || f.writes != 0 {
		t.Fatalf("save %+v", saved)
	}
	result, _ := m.Update(saved)
	m = result.(Model)
	if !m.BoardEditorOpen || m.BoardEditorNameInput.Value() != "Draft" || m.BoardEditorWriter == nil {
		t.Fatal("conflict discarded draft/observation")
	}
	m, cmd = m.executeBoardEditorDelete()
	if cmd == nil {
		t.Fatal("delete command missing")
	}
	deleted := cmd().(BoardEditorDeleteResultMsg)
	if !errors.As(deleted.Error, &conflict) || f.writes != 0 {
		t.Fatalf("delete %+v", deleted)
	}
	result, _ = m.Update(deleted)
	m = result.(Model)
	m.BoardEditorQueryInput.SetValue("future_field = x")
	next, cmd := m.executeBoardEditorSave()
	if !next.StatusIsError || cmd != nil {
		t.Fatal("invalid query opened writer")
	}
	m, _ = m.openBoardEditorCreate()
	m.BoardEditorNameInput.SetValue("New")
	m, cmd = m.executeBoardEditorSave()
	if cmd == nil {
		t.Fatal("create command missing")
	}
	created := cmd().(BoardEditorSaveResultMsg)
	if created.Error != nil || !created.IsNew || created.Board.Name != "New" || f.actor != "actual-monitor" {
		t.Fatalf("create %+v", created)
	}
}

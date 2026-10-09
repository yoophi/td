package monitor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/marcus/td/internal/db"
	"github.com/marcus/td/pkg/notes"
)

func noteUIModel(t *testing.T) Model {
	t.Helper()
	dir := t.TempDir()
	database, err := db.Initialize(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := NewModel(database, "actual", time.Minute, "test", dir)
	m.Width, m.Height = 100, 40
	t.Cleanup(func() { _ = m.Close() })
	return m
}
func noteUIComplete(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	if cmd == nil {
		t.Fatal("missing asynchronous command")
	}
	result, next := m.Update(cmd())
	if next != nil { // Delete queues its list refresh.
		result, _ = result.(Model).Update(next())
	}
	return result.(Model)
}
func TestNotesSQLiteFullUIFlowAndMultilineInput(t *testing.T) {
	m := noteUIModel(t)
	opened, cmd := m.openNotes()
	if !opened.NotesPending {
		t.Fatal("read not pending")
	}
	m = noteUIComplete(t, opened, cmd)
	if m.NotesState.Error != nil || len(m.NotesState.Notes) != 0 {
		t.Fatal(m.NotesState)
	}
	m, _ = m.handleNotesAction("create")
	m.NotesState.TitleInput.SetValue("한글 노트 📝")
	m.NotesState.ContentInput.SetValue("line1")
	m.NotesModal.SetFocus("note-content")
	_ = m.NotesModal.Render(m.Width, m.Height, m.NotesMouseHandler)
	updated, keyCmd := m.handleNotesUpdate(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	if m.NotesPending || m.NotesState.ContentInput.Value() != "line1\n" {
		t.Fatal("Enter submitted instead of newline", m.NotesState.ContentInput.Value(), keyCmd)
	}
	m, cmd = m.handleNotesAction("save")
	if !m.NotesPending || !m.NotesWriting {
		t.Fatal("save not pending")
	}
	if _, duplicate := m.handleNotesAction("save"); duplicate != nil {
		t.Fatal("duplicate save")
	}
	m = noteUIComplete(t, m, cmd)
	id := m.NotesState.DetailNote.ID
	if m.NotesState.Error != nil || !strings.HasPrefix(id, "nt-") || m.NotesState.Mode != "detail" {
		t.Fatal(m.NotesState)
	}
	m, cmd = m.handleNotesAction("toggle-pin")
	m = noteUIComplete(t, m, cmd)
	if !m.NotesState.DetailNote.Pinned {
		t.Fatal("pin missing")
	}
	m, cmd = m.handleNotesAction("toggle-archive")
	m = noteUIComplete(t, m, cmd)
	if !m.NotesState.DetailNote.Archived {
		t.Fatal("archive missing")
	}
	m, cmd = m.handleNotesAction("back")
	m = noteUIComplete(t, m, cmd)
	if len(m.NotesState.Notes) != 0 {
		t.Fatal("default archived filter")
	}
	m, cmd = m.handleNotesAction("toggle-archived")
	m = noteUIComplete(t, m, cmd)
	if len(m.NotesState.Notes) != 1 {
		t.Fatal("archived filter")
	}
	m.NotesState.SearchInput.SetValue("없는 제목")
	m, cmd = m.handleNotesAction("search")
	m = noteUIComplete(t, m, cmd)
	if len(m.NotesState.Notes) != 0 {
		t.Fatal("search ignored")
	}
	m.NotesState.SearchInput.SetValue("한글")
	m, cmd = m.handleNotesAction("search")
	m = noteUIComplete(t, m, cmd)
	m, cmd = m.handleNotesAction("open")
	m = noteUIComplete(t, m, cmd)
	m, _ = m.handleNotesAction("edit")
	m.NotesState.TitleInput.SetValue("수정")
	m.NotesState.ContentInput.SetValue("본문")
	m, cmd = m.handleNotesAction("save")
	m = noteUIComplete(t, m, cmd)
	if m.NotesState.DetailNote.Title != "수정" {
		t.Fatal("edit not saved")
	}
	m, _ = m.handleNotesAction("delete")
	if m.NotesState.Mode != "delete" {
		t.Fatal("delete lacks confirmation")
	}
	m, cmd = m.handleNotesAction("confirm-delete")
	m = noteUIComplete(t, m, cmd)
	if len(m.NotesState.Notes) != 0 {
		t.Fatal("deleted note visible")
	}
	m.NotesState.SearchInput.SetValue("")
	m, cmd = m.handleNotesAction("toggle-deleted")
	m = noteUIComplete(t, m, cmd)
	if len(m.NotesState.Notes) != 1 {
		t.Fatal("deleted list missing")
	}
	m, cmd = m.handleNotesAction("open")
	m = noteUIComplete(t, m, cmd)
	m, cmd = m.handleNotesAction("restore")
	m = noteUIComplete(t, m, cmd)
	if m.NotesState.DetailNote.DeletedAt != nil || !m.NotesState.DetailNote.Pinned || !m.NotesState.DetailNote.Archived {
		t.Fatal("restore lost flags")
	}
}

type uncertainNoteStore struct {
	MonitorNoteStore
	calls *int
}

func (s uncertainNoteStore) Create(string, string) (*notes.Note, error) {
	*s.calls++
	return nil, errors.New("unknown outcome; inspect GitHub before retrying")
}
func TestNotesUnknownCreateKeepsDraftAndBlocksResubmit(t *testing.T) {
	m := noteUIModel(t)
	calls := 0
	dir := m.BaseDir
	m.NotesFactory = func(ctx context.Context) (MonitorNoteStore, error) {
		s, e := notes.OpenWithContext(ctx, dir)
		return uncertainNoteStore{s, &calls}, e
	}
	m, cmd := m.openNotes()
	m = noteUIComplete(t, m, cmd)
	m, _ = m.handleNotesAction("create")
	m.NotesState.TitleInput.SetValue("retained draft")
	m, cmd = m.handleNotesAction("save")
	m = noteUIComplete(t, m, cmd)
	if m.NotesState.Mode != "create" || m.NotesState.TitleInput.Value() != "retained draft" || m.NotesState.Error == nil {
		t.Fatal("uncertain write lost draft")
	}
	m, cmd = m.handleNotesAction("save")
	if cmd != nil || calls != 1 {
		t.Fatal("uncertain creation retried")
	}
}
func TestNotesStaleResultsAndOwnerCancellation(t *testing.T) {
	initial := noteUIModel(t)
	m, oldCmd := initial.openNotes()
	old := oldCmd()
	m.closeNotes()
	m, newCmd := m.openNotes()
	m = noteUIComplete(t, m, newCmd)
	request := m.NotesRequest
	m, _ = m.applyNotesResult(old.(notesResultMsg))
	if m.NotesRequest != request || m.NotesPending {
		t.Fatal("old read changed fresh screen")
	}
	var captured context.Context
	m.NotesFactory = func(ctx context.Context) (MonitorNoteStore, error) { captured = ctx; return nil, ctx.Err() }
	m, cmd := m.requestNotes("list")
	if err := initial.Close(); err != nil {
		t.Fatal(err)
	}
	result := cmd().(notesResultMsg)
	if !errors.Is(result.Error, context.Canceled) || captured.Err() == nil {
		t.Fatal("initial model owner did not cancel request", result.Error)
	}
}
func TestNotesPendingWriteCanQuitAndDoesNotDiscardDraft(t *testing.T) {
	m := noteUIModel(t)
	m.DataSource = &GitHubDataSource{}
	m.NotesFactory = func(context.Context) (MonitorNoteStore, error) { return nil, errors.New("offline") }
	m, cmd := m.openNotes()
	m = noteUIComplete(t, m, cmd)
	m, _ = m.handleNotesAction("create")
	m.NotesState.TitleInput.SetValue("draft")
	m, cmd = m.handleNotesAction("save")
	if cmd == nil || !m.PendingRemoteWrite() {
		t.Fatal("pending write unreported")
	}
	result, quit := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if quit == nil {
		t.Fatal("Ctrl+C swallowed")
	}
	if _, ok := quit().(tea.QuitMsg); !ok {
		t.Fatal("did not quit")
	}
	if !result.(Model).PendingRemoteWrite() || result.(Model).NotesState.TitleInput.Value() != "draft" {
		t.Fatal("pending facts lost")
	}
}

type observedNoteStore struct {
	MonitorNoteStore
	observed **notes.Note
	writes   *int
}

func (s observedNoteStore) GetAny(id string) (*notes.Note, error) {
	n, e := s.MonitorNoteStore.GetAny(id)
	*s.observed = n
	return n, e
}
func (s observedNoteStore) UpdateObserved(n *notes.Note, title, content string) (*notes.Note, error) {
	if n != *s.observed || n.Title == title {
		return nil, errors.New("editor replaced original observation")
	}
	*s.writes++
	return nil, errors.New("conflict: note was changed by another writer")
}
func TestNotesEditorPreservesObservationAndConflictDraft(t *testing.T) {
	m := noteUIModel(t)
	s, e := notes.Open(m.BaseDir)
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.Create("original", "body")
	if e != nil {
		t.Fatal(e)
	}
	_ = s.Close()
	var observed *notes.Note
	writes := 0
	dir := m.BaseDir
	m.NotesFactory = func(ctx context.Context) (MonitorNoteStore, error) {
		s, e := notes.OpenWithContext(ctx, dir)
		return observedNoteStore{s, &observed, &writes}, e
	}
	m, cmd := m.openNotes()
	m = noteUIComplete(t, m, cmd)
	m, cmd = m.handleNotesAction("open")
	m = noteUIComplete(t, m, cmd)
	m, _ = m.handleNotesAction("edit")
	m.NotesState.TitleInput.SetValue("draft title")
	m.NotesState.ContentInput.SetValue("draft body")
	m, cmd = m.handleNotesAction("save")
	m = noteUIComplete(t, m, cmd)
	if writes != 1 || m.NotesState.Mode != "edit" || m.NotesState.TitleInput.Value() != "draft title" || m.NotesState.ContentInput.Value() != "draft body" || !strings.Contains(m.NotesState.Error.Error(), "conflict") || m.NotesState.Original != observed {
		t.Fatal("conflict concealed or draft lost", m.NotesState, writes)
	}
}

func TestNotesNBindingOpensFromDefaultBoardAndDetailScrolls(t *testing.T) {
	m := noteUIModel(t)
	m.TaskListMode = TaskListModeBoard
	m.ActivePanel = PanelTaskList
	result, cmd := m.handleKey(tea.KeyPressMsg{Code: 'N', Text: "N"})
	m = result.(Model)
	if !m.NotesOpen || cmd == nil {
		t.Fatal("N failed in board context")
	}
	m = noteUIComplete(t, m, cmd)
	m, _ = m.handleNotesAction("create")
	m.NotesState.TitleInput.SetValue("Long note")
	m.NotesState.ContentInput.SetValue(strings.Repeat("Long content line\n", 60))
	m, cmd = m.handleNotesAction("save")
	m = noteUIComplete(t, m, cmd)
	m.NotesModal.SetScrollOffset(0)
	result, _ = m.handleNotesUpdate(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	m = result.(Model)
	if m.NotesModal.ScrollOffset() != 5 || m.NotesState.Mode != "detail" {
		t.Fatal("detail paging unavailable")
	}
}

package monitor

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/pkg/monitor/modal"
	"github.com/marcus/td/pkg/monitor/mouse"
	"github.com/marcus/td/pkg/notes"
)

// MonitorNoteStore retains the observation originally shown by the UI.
// Each asynchronous request owns and closes its configured store.
type MonitorNoteStore interface {
	Close() error
	List(notes.ListOptions) ([]notes.Note, error)
	GetAny(string) (*notes.Note, error)
	Create(string, string) (*notes.Note, error)
	UpdateObserved(*notes.Note, string, string) (*notes.Note, error)
	SetPinnedObserved(*notes.Note, bool) (*notes.Note, error)
	SetArchivedObserved(*notes.Note, bool) (*notes.Note, error)
	DeleteObserved(*notes.Note) (*notes.Note, error)
	RestoreObserved(*notes.Note) (*notes.Note, error)
}

var _ MonitorNoteStore = (*notes.Store)(nil)

type notesResultMsg struct {
	Request   uint64
	Operation string
	Rows      []notes.Note
	Note      *notes.Note
	Error     error
}

func (m Model) openNotes() (Model, tea.Cmd) {
	if m.NotesPending {
		return m, nil
	}
	search := textinput.New()
	search.Placeholder = "title or content"
	m.NotesState = &NotesState{Mode: "list", SearchInput: &search, Observed: map[string]*notes.Note{}}
	m.NotesOpen = true
	m.NotesMouseHandler = mouse.NewHandler()
	return m.requestNotes("list")
}
func (m *Model) closeNotes() {
	if m.NotesCancel != nil {
		m.NotesCancel()
		m.NotesCancel = nil
	}
	m.NotesRequest++
	m.NotesPending = false
	m.NotesWriting = false
	m.NotesOpen = false
	m.NotesModal = nil
	m.NotesState = nil
	m.NotesMouseHandler = nil
}
func noteModel(n *notes.Note) models.Note {
	return models.Note{ID: n.ID, Title: n.Title, Content: n.Content, CreatedAt: n.CreatedAt, UpdatedAt: n.UpdatedAt, Pinned: n.Pinned, Archived: n.Archived, DeletedAt: n.DeletedAt}
}
func (m *Model) rebuildNotesModal() {
	ns := m.NotesState
	if ns == nil {
		return
	}
	focus := ""
	scroll := 0
	if m.NotesModal != nil {
		focus = m.NotesModal.FocusedID()
		scroll = m.NotesModal.ScrollOffset()
	}
	switch ns.Mode {
	case "create", "edit":
		title := "New note"
		if ns.Mode == "edit" {
			title = "Edit note"
		}
		md := m.newModal(title, ModalTypeNotes, modal.WithWidth(80), modal.WithHints(false), modal.WithPrimaryAction("save"))
		md.AddSection(modal.InputWithLabel("note-title", "Title:", ns.TitleInput, modal.WithSubmitOnEnter(false)))
		md.AddSection(modal.TextareaWithLabel("note-content", "Content:", ns.ContentInput, 6))
		md.AddSection(modal.Buttons(modal.Btn(" Save ", "save"), modal.Btn(" Cancel ", "cancel-edit")))
		md.AddSection(modal.Text("Tab: change field · Ctrl+S: save · Esc: cancel draft"))
		m.NotesModal = md
	case "delete":
		md := m.newModal("Delete note?", ModalTypeNotes, modal.WithWidth(60), modal.WithHints(false))
		md.AddSection(modal.Text("Logically delete this note? It can be restored from Deleted notes."))
		md.AddSection(modal.Buttons(modal.Btn(" Delete ", "confirm-delete", modal.BtnDanger()), modal.Btn(" Cancel ", "cancel-delete")))
		m.NotesModal = md
	case "detail":
		m.NotesModal = m.createNoteDetailModal()
	default:
		m.NotesModal = m.createNotesListModal()
	}
	if m.NotesModal == nil {
		return
	}
	if ns.Error != nil {
		m.NotesModal.AddSection(modal.Text("Error: " + ns.Error.Error()))
	}
	if m.NotesPending {
		label := "Loading notes… Esc: cancel read"
		if m.NotesWriting {
			label = "Saving note… Ctrl+C: quit; inspect GitHub if outcome is unknown"
		}
		m.NotesModal.AddSection(modal.Text(label))
	}
	if ns.Mode == "list" {
		m.NotesModal.AddSection(modal.Text("Enter: open · c: new · /: search · r: refresh · a: archived · d: deleted · Esc: close"))
	}
	if ns.Mode == "detail" {
		hints := "e: edit · p: pin · a: archive · d: delete · r: reload · Ctrl+D/U: scroll · Esc: back"
		if ns.DetailNote != nil && ns.DetailNote.DeletedAt != nil {
			hints = "Enter/d: restore · r: reload · Ctrl+D/U: scroll · Esc: back"
		}
		m.NotesModal.AddSection(modal.Text(hints))
	}
	m.NotesModal.Reset()
	_ = m.NotesModal.Render(m.Width, m.Height, m.NotesMouseHandler)
	if focus != "" {
		m.NotesModal.SetFocus(focus)
	} else if ns.Mode == "list" && len(ns.Notes) > 0 {
		m.NotesModal.SetFocus("notes-list")
	} else if ns.Mode == "list" {
		m.NotesModal.SetFocus("create")
	}
	m.NotesModal.SetScrollOffset(scroll)
}
func (m Model) requestNotes(operation string) (Model, tea.Cmd) {
	if m.NotesPending || m.NotesState == nil {
		return m, nil
	}
	ns := m.NotesState
	writing := operation != "list" && operation != "get"
	if operation == "create" && ns.CreateAttempted {
		ns.Error = fmt.Errorf("creation was already attempted; inspect the repository before starting a new draft")
		m.rebuildNotesModal()
		return m, nil
	}
	if operation == "create" || operation == "edit" {
		if ns.TitleInput == nil || strings.TrimSpace(ns.TitleInput.Value()) == "" {
			ns.Error = fmt.Errorf("note title is required")
			m.rebuildNotesModal()
			return m, nil
		}
	}
	parent := m.NotesContext
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	m.NotesCancel = cancel
	m.NotesRequest++
	request := m.NotesRequest
	m.NotesPending = true
	m.NotesWriting = writing
	ns.Error = nil
	if operation == "create" {
		ns.CreateAttempted = true
	}
	factory := m.NotesFactory
	if factory == nil {
		base := m.BaseDir
		if base == "" && m.DB != nil {
			base = m.DB.BaseDir()
		}
		remote := m.DataSource != nil
		factory = func(ctx context.Context) (MonitorNoteStore, error) {
			if remote {
				return nil, fmt.Errorf("GitHub notes provider is unavailable")
			}
			store, err := notes.OpenWithContext(ctx, base)
			if err != nil {
				return nil, err
			}
			if store.Kind() != "sqlite" {
				_ = store.Close()
				return nil, fmt.Errorf("notes store configuration changed; restart the monitor")
			}
			return store, nil
		}
	}
	opts := notes.ListOptions{IncludeDeleted: ns.ShowDeleted}
	if ns.SearchInput != nil {
		opts.Search = ns.SearchInput.Value()
	}
	if !ns.ShowArchived && !ns.ShowDeleted {
		archived := false
		opts.Archived = &archived
	}
	original := ns.Original
	id := ""
	if ns.DetailNote != nil {
		id = ns.DetailNote.ID
	}
	title, content := "", ""
	if ns.TitleInput != nil {
		title = ns.TitleInput.Value()
	}
	if ns.ContentInput != nil {
		content = ns.ContentInput.Value()
	}
	deletedOnly := ns.ShowDeleted
	m.rebuildNotesModal()
	return m, func() tea.Msg {
		defer cancel()
		result := notesResultMsg{Request: request, Operation: operation}
		store, err := factory(ctx)
		if err != nil {
			result.Error = err
			return result
		}
		defer func() { _ = store.Close() }()
		switch operation {
		case "list":
			result.Rows, result.Error = store.List(opts)
			if deletedOnly && result.Error == nil {
				kept := []notes.Note{}
				for _, n := range result.Rows {
					if n.DeletedAt != nil {
						kept = append(kept, n)
					}
				}
				result.Rows = kept
			}
		case "get":
			result.Note, result.Error = store.GetAny(id)
		case "create":
			result.Note, result.Error = store.Create(title, content)
		case "edit":
			result.Note, result.Error = store.UpdateObserved(original, title, content)
		case "pin":
			if original == nil {
				result.Error = fmt.Errorf("note observation is missing")
			} else {
				result.Note, result.Error = store.SetPinnedObserved(original, !original.Pinned)
			}
		case "archive":
			if original == nil {
				result.Error = fmt.Errorf("note observation is missing")
			} else {
				result.Note, result.Error = store.SetArchivedObserved(original, !original.Archived)
			}
		case "delete":
			result.Note, result.Error = store.DeleteObserved(original)
		case "restore":
			result.Note, result.Error = store.RestoreObserved(original)
		default:
			result.Error = fmt.Errorf("unknown note operation %q", operation)
		}
		return result
	}
}
func (m Model) applyNotesResult(msg notesResultMsg) (Model, tea.Cmd) {
	if !m.NotesOpen || m.NotesState == nil || msg.Request != m.NotesRequest {
		return m, nil
	}
	ns := m.NotesState
	m.NotesPending = false
	m.NotesWriting = false
	m.NotesCancel = nil
	ns.Error = msg.Error
	if msg.Error != nil {
		m.StatusMessage = "Notes: " + msg.Error.Error()
		m.StatusIsError = true
		m.rebuildNotesModal()
		return m, nil
	}
	if msg.Operation == "list" {
		selected := ""
		if ns.ListCursor >= 0 && ns.ListCursor < len(ns.Notes) {
			selected = ns.Notes[ns.ListCursor].ID
		}
		ns.Notes = []models.Note{}
		ns.Observed = map[string]*notes.Note{}
		for i := range msg.Rows {
			n := msg.Rows[i]
			ns.Notes = append(ns.Notes, noteModel(&n))
			ns.Observed[n.ID] = &n
		}
		ns.ListCursor = 0
		for i, n := range ns.Notes {
			if n.ID == selected {
				ns.ListCursor = i
			}
		}
		ns.Mode = "list"
	} else {
		if msg.Note == nil {
			ns.Error = fmt.Errorf("notes provider returned no saved note")
			m.rebuildNotesModal()
			return m, nil
		}
		display := noteModel(msg.Note)
		ns.DetailNote = &display
		ns.Original = msg.Note
		ns.DetailRender = preRenderMarkdown(display.Content, m.modalContentWidth(), m.MarkdownTheme)
		ns.Mode = "detail"
		if msg.Operation == "delete" {
			ns.Mode = "list"
			m.NotesModal = nil
			return m.requestNotes("list")
		}
	}
	m.StatusMessage = "Notes updated"
	m.StatusIsError = false
	m.NotesModal = nil
	m.rebuildNotesModal()
	return m, nil
}
func (m Model) handleNotesAction(action string) (Model, tea.Cmd) {
	ns := m.NotesState
	if ns == nil {
		return m, nil
	}
	if m.NotesPending {
		return m, nil
	}
	switch action {
	case "cancel":
		result, cmd := m.handleNotesUpdate(tea.KeyPressMsg{Code: tea.KeyEscape})
		return result.(Model), cmd
	case "close":
		m.closeNotes()
		return m, nil
	case "refresh", "search":
		ns.Mode = "list"
		return m.requestNotes("list")
	case "toggle-archived":
		ns.ShowArchived = !ns.ShowArchived
		return m.requestNotes("list")
	case "toggle-deleted":
		ns.ShowDeleted = !ns.ShowDeleted
		return m.requestNotes("list")
	case "back":
		ns.Mode = "list"
		m.NotesModal = nil
		return m.requestNotes("list")
	case "create", "edit":
		if action == "edit" && (ns.Original == nil || ns.Original.DeletedAt != nil) {
			return m, nil
		}
		title := textinput.New()
		title.CharLimit = 0
		body := textarea.New()
		body.CharLimit = 0
		ns.CreateAttempted = false
		if action == "edit" {
			title.SetValue(ns.Original.Title)
			body.SetValue(ns.Original.Content)
		} else {
			ns.Original = nil
			ns.DetailNote = nil
		}
		ns.Mode = action
		ns.TitleInput = &title
		ns.ContentInput = &body
		ns.Error = nil
		m.NotesModal = nil
		m.rebuildNotesModal()
		return m, title.Focus()
	case "save":
		return m.requestNotes(ns.Mode)
	case "cancel-edit":
		if ns.Original == nil {
			ns.Mode = "list"
		} else {
			ns.Mode = "detail"
		}
		ns.Error = nil
		m.NotesModal = nil
		m.rebuildNotesModal()
		return m, nil
	case "toggle-pin":
		return m.requestNotes("pin")
	case "toggle-archive":
		return m.requestNotes("archive")
	case "restore":
		return m.requestNotes("restore")
	case "delete":
		ns.Mode = "delete"
		m.NotesModal = nil
		m.rebuildNotesModal()
		return m, nil
	case "confirm-delete":
		return m.requestNotes("delete")
	case "cancel-delete":
		ns.Mode = "detail"
		m.NotesModal = nil
		m.rebuildNotesModal()
		return m, nil
	case "open":
		if ns.ListCursor < 0 || ns.ListCursor >= len(ns.Notes) {
			return m, nil
		}
		display := ns.Notes[ns.ListCursor]
		ns.DetailNote = &display
		ns.Original = ns.Observed[display.ID]
		return m.requestNotes("get")
	default:
		if strings.HasPrefix(action, "note-") {
			index, err := strconv.Atoi(strings.TrimPrefix(action, "note-"))
			if err == nil && index >= 0 && index < len(ns.Notes) {
				ns.ListCursor = index
				return m.handleNotesAction("open")
			}
		}
	}
	return m, nil
}
func (m Model) handleNotesUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	ns := m.NotesState
	if ns == nil {
		return m, nil
	}
	if key, ok := msg.(tea.KeyMsg); ok {
		k := key.String()
		if k == "ctrl+c" {
			return m, tea.Quit
		}
		if k == "esc" {
			if m.NotesPending && m.NotesWriting {
				ns.Error = fmt.Errorf("save is pending; Ctrl+C quits, then inspect GitHub for the outcome")
				m.rebuildNotesModal()
				return m, nil
			}
			if m.NotesPending {
				m.closeNotes()
				return m, nil
			}
			if ns.Mode == "create" || ns.Mode == "edit" {
				return m.handleNotesAction("cancel-edit")
			}
			if ns.Mode == "delete" {
				return m.handleNotesAction("cancel-delete")
			}
			if ns.Mode == "detail" {
				return m.handleNotesAction("back")
			}
			m.closeNotes()
			return m, nil
		}
		if m.NotesPending {
			return m, nil
		}
		if k == "ctrl+s" && (ns.Mode == "edit" || ns.Mode == "create") {
			return m.handleNotesAction("save")
		}
		if k == "enter" && (ns.Mode == "edit" || ns.Mode == "create") && m.NotesModal != nil {
			switch m.NotesModal.FocusedID() {
			case "note-title":
				m.NotesModal.SetFocus("note-content")
				return m, ns.ContentInput.Focus()
			case "note-content":
				_, cmd := m.NotesModal.HandleMsg(key)
				return m, cmd
			}
		}
		if ns.Mode == "list" && m.NotesModal.FocusedID() != "note-search" {
			switch k {
			case "c":
				return m.handleNotesAction("create")
			case "/":
				m.NotesModal.SetFocus("note-search")
				return m, ns.SearchInput.Focus()
			case "r":
				return m.handleNotesAction("refresh")
			case "a":
				return m.handleNotesAction("toggle-archived")
			case "d":
				return m.handleNotesAction("toggle-deleted")
			case "enter":
				if m.NotesModal.FocusedID() == "notes-list" {
					return m.handleNotesAction("open")
				}
			}
		}
		if ns.Mode == "detail" {
			switch k {
			case "j", "down":
				m.NotesModal.Scroll(1)
				return m, nil
			case "k", "up":
				m.NotesModal.Scroll(-1)
				return m, nil
			case "ctrl+d", "pgdown", "ctrl+f":
				m.NotesModal.Scroll(5)
				return m, nil
			case "ctrl+u", "pgup", "ctrl+b":
				m.NotesModal.Scroll(-5)
				return m, nil
			case "home":
				m.NotesModal.SetScrollOffset(0)
				return m, nil
			case "end":
				m.NotesModal.SetScrollOffset(100000)
				return m, nil
			case "e":
				return m.handleNotesAction("edit")
			case "p":
				return m.handleNotesAction("toggle-pin")
			case "a":
				return m.handleNotesAction("toggle-archive")
			case "d":
				if ns.Original != nil && ns.Original.DeletedAt != nil {
					return m.handleNotesAction("restore")
				}
				return m.handleNotesAction("delete")
			case "r":
				return m.requestNotes("get")
			}
		}
		if m.NotesModal != nil {
			action, cmd := m.NotesModal.HandleKey(key)
			if action != "" {
				return m.handleNotesAction(action)
			}
			return m, cmd
		}
	}
	if m.NotesPending {
		return m, nil
	}
	if event, ok := msg.(tea.MouseMsg); ok && m.NotesModal != nil {
		action := m.NotesModal.HandleMouse(event, m.NotesMouseHandler)
		if action != "" {
			return m.handleNotesAction(action)
		}
		return m, nil
	}
	if m.NotesModal != nil {
		_, cmd := m.NotesModal.HandleMsg(msg)
		return m, cmd
	}
	return m, nil
}

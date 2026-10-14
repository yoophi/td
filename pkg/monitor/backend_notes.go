package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/internal/issuestore"
	"github.com/marcus/td/pkg/notes"
)

type backendNoteStore struct {
	ctx   context.Context
	conn  *backendstore.Connection
	guard func(context.Context) error
}

var _ MonitorNoteStore = (*backendNoteStore)(nil)

func (s *backendNoteStore) Close() error { return nil }

func backendNote(row backendstore.Note) *notes.Note {
	return &notes.Note{ID: row.ID, Title: row.Title, Content: row.Content, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, Pinned: row.Pinned, Archived: row.Archived, DeletedAt: row.DeletedAt, BackendRevision: row.Revision}
}

func (s *backendNoteStore) List(opts notes.ListOptions) ([]notes.Note, error) {
	if err := s.guard(s.ctx); err != nil {
		return nil, err
	}
	var rows []backendstore.Note
	var err error
	if opts.IncludeDeleted {
		rows, err = s.conn.NotesIncludingDeleted(s.ctx)
	} else {
		rows, err = s.conn.Notes(s.ctx)
	}
	if err != nil {
		return nil, err
	}
	if err := s.guard(s.ctx); err != nil {
		return nil, err
	}
	match := issuestore.SQLiteLIKE("%" + opts.Search + "%")
	out := make([]notes.Note, 0, len(rows))
	for _, row := range rows {
		n := backendNote(row)
		if opts.Pinned != nil && n.Pinned != *opts.Pinned || opts.Archived != nil && n.Archived != *opts.Archived || opts.Search != "" && !match(n.Title) && !match(n.Content) {
			continue
		}
		out = append(out, *n)
	}
	slices.SortFunc(out, func(a, b notes.Note) int {
		if a.Pinned != b.Pinned {
			if a.Pinned {
				return -1
			}
			return 1
		}
		if c := b.UpdatedAt.Compare(a.UpdatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	if opts.Limit > 0 && len(out) > opts.Limit {
		out = out[:opts.Limit]
	}
	return out, nil
}

func (s *backendNoteStore) GetAny(id string) (*notes.Note, error) {
	if err := s.guard(s.ctx); err != nil {
		return nil, err
	}
	row, err := s.conn.NoteIncludingDeleted(s.ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.guard(s.ctx); err != nil {
		return nil, err
	}
	return backendNote(row), nil
}

func (s *backendNoteStore) Create(title, content string) (*notes.Note, error) {
	if strings.TrimSpace(title) == "" {
		return nil, fmt.Errorf("note title is required")
	}
	var reply struct {
		Note backendstore.Note `json:"note"`
	}
	input := struct{ Kind, Title, Content string }{"note.create", title, content}
	err := s.conn.RunIntent(s.ctx, input, func() (backendstore.MutationRequest, error) {
		if err := s.guard(s.ctx); err != nil {
			return backendstore.MutationRequest{}, err
		}
		body, err := json.Marshal(map[string]any{"title": title, "content": content, "pinned": false})
		return backendstore.MutationRequest{Method: "POST", Resource: "/notes", Body: body}, err
	}, &reply, func() error { return backendstore.ValidateNote(reply.Note) })
	if err != nil {
		return nil, err
	}
	return backendNote(reply.Note), nil
}

func (s *backendNoteStore) UpdateObserved(n *notes.Note, title, content string) (*notes.Note, error) {
	if strings.TrimSpace(title) == "" {
		return nil, fmt.Errorf("note title is required")
	}
	return s.mutateObserved(n, "edit", "PATCH", map[string]any{"title": title, "content": content})
}

func (s *backendNoteStore) SetPinnedObserved(n *notes.Note, pinned bool) (*notes.Note, error) {
	return s.mutateObserved(n, "pin", "PATCH", map[string]any{"pinned": pinned})
}

func (s *backendNoteStore) SetArchivedObserved(n *notes.Note, archived bool) (*notes.Note, error) {
	return s.mutateObserved(n, "archive", "PATCH", map[string]any{"archived": archived})
}

func (s *backendNoteStore) DeleteObserved(n *notes.Note) (*notes.Note, error) {
	return s.mutateObserved(n, "delete", "DELETE", nil)
}

func (s *backendNoteStore) RestoreObserved(n *notes.Note) (*notes.Note, error) {
	if n == nil || n.DeletedAt == nil {
		return nil, fmt.Errorf("deleted note observation required")
	}
	return s.mutateObserved(n, "restore", "POST", nil)
}

func (s *backendNoteStore) mutateObserved(n *notes.Note, action, method string, payload any) (*notes.Note, error) {
	if n == nil || n.ID == "" || n.BackendRevision == "" || strings.ContainsAny(n.ID, "/?#") {
		return nil, fmt.Errorf("backend note observation required")
	}
	resource := "/notes/" + n.ID
	if action == "restore" {
		resource += "/restore"
	}
	input := struct {
		Kind, ID, Revision, Method string
		Body                       any
	}{"note." + action, n.ID, n.BackendRevision, method, payload}
	var reply struct {
		Note    backendstore.Note `json:"note"`
		Deleted bool              `json:"deleted"`
	}
	err := s.conn.RunIntent(s.ctx, input, func() (backendstore.MutationRequest, error) {
		request := backendstore.MutationRequest{}
		if err := s.guard(s.ctx); err != nil {
			return request, err
		}
		current, err := s.conn.NoteIncludingDeleted(s.ctx, n.ID)
		if err != nil {
			return request, err
		}
		if err := s.guard(s.ctx); err != nil {
			return request, err
		}
		if current.Revision != n.BackendRevision || (current.DeletedAt != nil) != (n.DeletedAt != nil) {
			return request, fmt.Errorf("note changed since it was displayed; refresh before editing")
		}
		request = backendstore.MutationRequest{Method: method, Resource: resource, Revision: n.BackendRevision}
		if payload != nil {
			request.Body, err = json.Marshal(payload)
		}
		return request, err
	}, &reply, func() error {
		if action == "delete" {
			if !reply.Deleted {
				return fmt.Errorf("backend did not confirm note deletion")
			}
			return nil
		}
		if err := backendstore.ValidateNote(reply.Note); err != nil {
			return err
		}
		if reply.Note.ID != n.ID {
			return fmt.Errorf("backend returned a different note")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if action == "delete" {
		copy := *n
		deletedAt := time.Now().UTC()
		copy.DeletedAt = &deletedAt
		return &copy, nil
	}
	return backendNote(reply.Note), nil
}

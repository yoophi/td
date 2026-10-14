package backendstore

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"
)

type Note struct {
	ID        string     `json:"id"`
	Title     string     `json:"title"`
	Content   string     `json:"content"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	Pinned    bool       `json:"pinned"`
	Archived  bool       `json:"archived"`
	DeletedAt *time.Time `json:"deleted_at"`
	Revision  string     `json:"revision"`
}

func ValidateNote(n Note) error {
	if n.ID == "" || strings.ContainsAny(n.ID, "/?#") || n.Title == "" || !revisionPattern.MatchString(n.Revision) || n.CreatedAt.IsZero() || n.UpdatedAt.IsZero() {
		return fmt.Errorf("invalid backend note")
	}
	return nil
}

func (c *Connection) Notes(ctx context.Context) ([]Note, error) {
	return c.listNotes(ctx, false)
}

func (c *Connection) NotesIncludingDeleted(ctx context.Context) ([]Note, error) {
	return c.listNotes(ctx, true)
}

func (c *Connection) listNotes(ctx context.Context, includeDeleted bool) ([]Note, error) {
	path, e := scopedPath(c.ProjectID, c.SessionID)
	if e != nil {
		return nil, e
	}
	var reply struct {
		Notes []Note `json:"notes"`
	}
	path += "/notes"
	if includeDeleted {
		path += "?include_deleted=true"
	}
	if e = c.Client.request(ctx, "GET", path, c.SessionID, "", nil, &reply); e != nil {
		return nil, e
	}
	if reply.Notes == nil {
		return nil, fmt.Errorf("invalid backend note list")
	}
	seen := map[string]bool{}
	for _, n := range reply.Notes {
		if e = ValidateNote(n); e != nil || seen[n.ID] || !includeDeleted && n.DeletedAt != nil {
			return nil, fmt.Errorf("invalid backend note list")
		}
		seen[n.ID] = true
	}
	return reply.Notes, nil
}

func ResolveNote(rows []Note, raw string) (Note, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Note{}, fmt.Errorf("note ID required")
	}
	var matches []Note
	for _, n := range rows {
		if n.ID == raw {
			return n, nil
		}
		if strings.HasPrefix(n.ID, raw) {
			matches = append(matches, n)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return Note{}, fmt.Errorf("ambiguous note ID %q", raw)
	}
	return Note{}, fmt.Errorf("note %q not found", raw)
}

func (c *Connection) Note(ctx context.Context, id string) (Note, error) {
	return c.getNote(ctx, id, false)
}

func (c *Connection) NoteIncludingDeleted(ctx context.Context, id string) (Note, error) {
	return c.getNote(ctx, id, true)
}

func (c *Connection) getNote(ctx context.Context, id string, includeDeleted bool) (Note, error) {
	path, e := scopedPath(c.ProjectID, c.SessionID)
	if e != nil {
		return Note{}, e
	}
	if id == "" || strings.ContainsAny(id, "/?#") {
		return Note{}, fmt.Errorf("invalid backend note ID")
	}
	var reply struct {
		Note Note `json:"note"`
	}
	path += "/notes/" + url.PathEscape(id)
	if includeDeleted {
		path += "?include_deleted=true"
	}
	if e = c.Client.request(ctx, "GET", path, c.SessionID, "", nil, &reply); e != nil {
		return Note{}, e
	}
	if e = ValidateNote(reply.Note); e != nil || reply.Note.ID != id || !includeDeleted && reply.Note.DeletedAt != nil {
		return Note{}, fmt.Errorf("invalid backend note detail")
	}
	return reply.Note, nil
}

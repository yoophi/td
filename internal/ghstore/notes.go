package ghstore

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/marcus/td/internal/models"
)

// NoteDetails belongs only to a note carrier. Native close/reopen does not
// archive or delete a note. Title, visible content and clocks remain GitHub's.
type NoteDetails struct {
	Version   int         `json:"version"`
	Pinned    bool        `json:"pinned,omitempty"`
	Archived  bool        `json:"archived,omitempty"`
	DeletedAt *time.Time  `json:"deleted_at,omitempty"`
	History   []NoteEvent `json:"history,omitempty"`
}
type NoteEvent struct {
	OperationID string    `json:"operation_id"`
	Action      string    `json:"action"`
	SessionID   string    `json:"session_id"`
	At          time.Time `json:"at"`
}

func (d NoteDetails) validate() error {
	if d.Version != 1 {
		return fmt.Errorf("unsupported note metadata version %d", d.Version)
	}
	if d.DeletedAt != nil && d.DeletedAt.IsZero() {
		return fmt.Errorf("note deletion timestamp must not be zero")
	}
	seen := map[string]bool{}
	for _, e := range d.History {
		if e.OperationID == "" || seen[e.OperationID] || strings.TrimSpace(e.SessionID) == "" || e.At.IsZero() || !slices.Contains([]string{"create", "update", "pin", "unpin", "archive", "unarchive", "delete", "restore"}, e.Action) {
			return fmt.Errorf("invalid or duplicate note history")
		}
		seen[e.OperationID] = true
	}
	return nil
}

// NoteRecord retains a private observation. Changing public display fields
// never substitutes a newer observation or changes unrelated stored flags.
type NoteRecord struct {
	models.Note
	Number   int    `json:"number"`
	URL      string `json:"url"`
	observed Record
}

func NoteNumber(id string) (int, error) {
	if !strings.HasPrefix(id, "nt-gh-") {
		return 0, fmt.Errorf("GitHub note ID must be nt-gh-N, got %q", id)
	}
	n, err := strconv.Atoi(strings.TrimPrefix(id, "nt-gh-"))
	if err != nil || n <= 0 || id != fmt.Sprintf("nt-gh-%d", n) {
		return 0, fmt.Errorf("GitHub note ID must be canonical nt-gh-N, got %q", id)
	}
	return n, nil
}

func noteRecord(record *Record) (*NoteRecord, error) {
	if record == nil || record.meta.EntityKind != "note" || record.meta.Note == nil {
		return nil, fmt.Errorf("carrier is not a td note")
	}
	d := record.meta.Note
	n := &NoteRecord{Note: models.Note{ID: fmt.Sprintf("nt-gh-%d", record.Number), Title: record.Title, Content: record.Description, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt, Pinned: d.Pinned, Archived: d.Archived}, Number: record.Number, URL: record.URL, observed: *record}
	if d.DeletedAt != nil {
		stamp := *d.DeletedAt
		n.DeletedAt = &stamp
	}
	return n, nil
}

func (c *Client) GetNote(ctx context.Context, id string, includeDeleted bool) (*NoteRecord, error) {
	n, err := NoteNumber(id)
	if err != nil {
		return nil, err
	}
	note, err := c.noteCarrier(ctx, n)
	if err != nil {
		return nil, err
	}
	if note.DeletedAt != nil && !includeDeleted {
		return nil, fmt.Errorf("note %s is deleted; use --include-deleted or restore it", id)
	}
	return note, nil
}
func (c *Client) noteCarrier(ctx context.Context, number int) (*NoteRecord, error) {
	data, err := c.request(ctx, "GET", fmt.Sprintf("/issues/%d", number), nil, false)
	if err != nil {
		return nil, err
	}
	record, err := decodeIssue(data)
	if err != nil {
		return nil, err
	}
	if record.Number != number {
		return nil, fmt.Errorf("note carrier identity mismatch")
	}
	record.repository = c.repo
	return noteRecord(record)
}

func (c *Client) ListNotes(ctx context.Context, includeDeleted bool) ([]NoteRecord, error) {
	data, err := c.request(ctx, "GET", "/issues?state=all&per_page=100", nil, true)
	if err != nil {
		return nil, err
	}
	var pages [][]apiIssue
	if err := json.Unmarshal(data, &pages); err != nil {
		return nil, fmt.Errorf("invalid GitHub note list: %w", err)
	}
	result := []NoteRecord{}
	seen := map[int]bool{}
	for _, page := range pages {
		for _, item := range page {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if len(item.PullRequest) > 0 && string(item.PullRequest) != "null" {
				continue
			}
			record, err := item.record()
			if err != nil {
				return nil, err
			}
			if record.meta.EntityKind != "note" {
				continue
			}
			if seen[item.Number] {
				return nil, fmt.Errorf("note pagination repeated #%d; retry the read", item.Number)
			}
			seen[item.Number] = true
			record.repository = c.repo
			note, err := noteRecord(record)
			if err != nil {
				return nil, err
			}
			if includeDeleted || note.DeletedAt == nil {
				result = append(result, *note)
			}
		}
	}
	return result, nil
}

func (c *Client) CreateNote(ctx context.Context, title, content, actor string) (*NoteRecord, error) {
	title = strings.TrimSpace(title)
	if title == "" || strings.TrimSpace(actor) == "" {
		return nil, fmt.Errorf("note creation requires a title and actual session")
	}
	operation := "td-note-op-" + rand.Text()
	d := NoteDetails{Version: 1, History: []NoteEvent{{OperationID: operation, Action: "create", SessionID: actor, At: time.Now().UTC()}}}
	body, err := encodeBody(content, metadata{EntityKind: "note", Note: &d, OperationID: operation, Type: models.TypeTask, Priority: models.PriorityP2})
	if err != nil {
		return nil, err
	}
	data, err := c.request(ctx, "POST", "/issues", map[string]any{"title": title, "body": body}, false)
	if err != nil {
		return nil, fmt.Errorf("create note operation %s failed; outcome may be unknown, inspect GitHub before retrying: %w", operation, err)
	}
	record, err := decodeIssue(data)
	if err != nil {
		return nil, fmt.Errorf("note creation operation %s response unreadable; inspect GitHub before retrying: %w", operation, err)
	}
	record.repository = c.repo
	note, err := noteRecord(record)
	if err != nil {
		return nil, fmt.Errorf("note creation operation %s returned invalid carrier; inspect GitHub before retrying: %w", operation, err)
	}
	return c.confirmNote(ctx, note, data, title, body)
}

type NoteChanges struct {
	Title, Content   *string
	Pinned, Archived *bool
	Deleted          *bool
}

// UpdateNoteObserved writes one title/body patch, preserving native state and
// labels. Revision checks are best-effort; uncertain writes are never retried.
func (c *Client) UpdateNoteObserved(ctx context.Context, observed *NoteRecord, changes NoteChanges, actor string) (*NoteRecord, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if observed == nil || strings.TrimSpace(actor) == "" {
		return nil, false, fmt.Errorf("note update requires an observation and actual session")
	}
	original := observed.observed
	if original.repository != c.repo || original.revision == ([32]byte{}) || original.Number <= 0 || observed.Number != original.Number {
		return nil, false, fmt.Errorf("note observation is not from this repository")
	}
	before, err := noteRecord(&original)
	if err != nil || observed.ID != before.ID {
		return nil, false, fmt.Errorf("note observation identity is inconsistent")
	}
	if before.DeletedAt != nil && (changes.Deleted == nil || changes.Title != nil || changes.Content != nil || changes.Pinned != nil || changes.Archived != nil) {
		return nil, false, fmt.Errorf("note %s is deleted; restore before editing", before.ID)
	}
	current, err := c.noteCarrier(ctx, original.Number)
	if err != nil {
		return nil, false, err
	}
	if current.observed.revision != original.revision {
		return nil, false, &ConflictError{ID: before.ID}
	}
	// Reconstruct private metadata to avoid aliasing observation history.
	var details NoteDetails
	raw, _ := json.Marshal(original.meta.Note)
	if err := json.Unmarshal(raw, &details); err != nil {
		return nil, false, err
	}
	title, content, action := before.Title, before.Content, "update"
	if changes.Title != nil {
		title = strings.TrimSpace(*changes.Title)
		if title == "" {
			return nil, false, fmt.Errorf("note title must not be empty")
		}
	}
	if changes.Content != nil {
		content = *changes.Content
	}
	if changes.Pinned != nil {
		details.Pinned = *changes.Pinned
		action = "unpin"
		if details.Pinned {
			action = "pin"
		}
	}
	if changes.Archived != nil {
		details.Archived = *changes.Archived
		action = "unarchive"
		if details.Archived {
			action = "archive"
		}
	}
	if changes.Deleted != nil {
		action = "restore"
		if *changes.Deleted {
			action = "delete"
			if details.DeletedAt == nil {
				stamp := time.Now().UTC()
				details.DeletedAt = &stamp
			}
		} else {
			details.DeletedAt = nil
		}
	}
	deleted := details.DeletedAt != nil
	if title == before.Title && content == before.Content && details.Pinned == before.Pinned && details.Archived == before.Archived && deleted == (before.DeletedAt != nil) {
		return current, false, nil
	}
	operation := "td-note-op-" + rand.Text()
	details.History = append(details.History, NoteEvent{OperationID: operation, Action: action, SessionID: actor, At: time.Now().UTC()})
	meta := original.meta
	meta.Note, meta.OperationID = &details, operation
	body, err := encodeBody(content, meta)
	if err != nil {
		return nil, false, err
	}
	data, err := c.request(ctx, "PATCH", fmt.Sprintf("/issues/%d", original.Number), map[string]any{"title": title, "body": body}, false)
	if err != nil {
		return nil, false, fmt.Errorf("note %s operation %s failed; outcome may be unknown, inspect GitHub before retrying: %w", before.ID, operation, err)
	}
	record, err := decodeIssue(data)
	if err != nil {
		return nil, false, fmt.Errorf("note %s write response invalid; inspect GitHub before retrying: %w", before.ID, err)
	}
	if record.Number != original.Number {
		return nil, false, fmt.Errorf("note %s write response invalid; inspect GitHub before retrying", before.ID)
	}
	record.repository = c.repo
	result, err := noteRecord(record)
	if err != nil {
		return nil, false, fmt.Errorf("note %s write returned invalid metadata; inspect GitHub before retrying: %w", before.ID, err)
	}
	confirmed, err := c.confirmNote(ctx, result, data, title, body)
	return confirmed, err == nil, err
}

func (c *Client) confirmNote(ctx context.Context, note *NoteRecord, data []byte, title, body string) (*NoteRecord, error) {
	var returned struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if err := json.Unmarshal(data, &returned); err != nil {
		return nil, fmt.Errorf("note %s write response unreadable; inspect GitHub before retrying: %w", note.ID, err)
	}
	if returned.Title != title || returned.Body != body {
		return nil, fmt.Errorf("note %s write was accepted but title/body differed; inspect GitHub before retrying", note.ID)
	}
	confirmed, err := c.noteCarrier(ctx, note.Number)
	if err != nil {
		return nil, fmt.Errorf("note %s write was accepted but verification failed; inspect GitHub before retrying: %w", note.ID, err)
	}
	if confirmed.observed.revision != note.observed.revision {
		return nil, &ConflictError{ID: note.ID, AfterWrite: true}
	}
	return confirmed, nil
}

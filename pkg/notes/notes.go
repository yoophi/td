// Package notes is the public Go API for td notes.
//
// Sidecar and other in-process clients should open a Store and call these
// methods instead of speaking SQL. Open selects the configured SQLite or
// GitHub store; there is no fallback between them.
package notes

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/db"
	"github.com/marcus/td/internal/ghcontext"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/issuestore"
	"github.com/marcus/td/internal/models"
)

// Note is a freeform note. Copies retain the private GitHub read observation;
// UpdateObserved preserves that observation across an external editor or UI.
type Note struct {
	ID        string     `json:"id"`
	Title     string     `json:"title"`
	Content   string     `json:"content"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	Pinned    bool       `json:"pinned"`
	Archived  bool       `json:"archived"`
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
	observed  *ghstore.NoteRecord
}

// ListOptions filters List. Limit <= 0 means unlimited.
type ListOptions struct {
	Pinned         *bool
	Archived       *bool
	IncludeDeleted bool
	Search         string
	Limit          int
}

// Store is a long-lived handle on a project's notes.
type Store struct {
	db       *db.DB
	ctx      context.Context
	cancel   context.CancelFunc
	baseDir  string
	selected *models.GitHubStoreConfig
	scope    ghcontext.Scope
	actor    string
}

// Open opens the configured notes store at baseDir (resolving .td-root).
func Open(baseDir string) (*Store, error) {
	return OpenWithContext(context.Background(), baseDir)
}

// OpenWithContext validates the selected remote before returning and owns
// cancellation. Pass the actual worktree directory, not a canonical checkout.
func OpenWithContext(ctx context.Context, baseDir string) (*Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if baseDir == "" {
		var err error
		baseDir, err = os.Getwd()
		if err != nil {
			return nil, err
		}
	}
	resolved := db.ResolveBaseDir(baseDir)
	cfg, err := config.Load(resolved)
	if err != nil {
		return nil, err
	}
	kind, err := config.Store(cfg)
	if err != nil {
		return nil, err
	}
	if kind == config.StoreGitHub {
		if _, err := ghstore.Open(ctx, resolved, cfg.GitHub); err != nil {
			return nil, err
		}
		scope, err := ghcontext.Resolve(ctx, baseDir, cfg.GitHub.Repo)
		if err != nil {
			return nil, err
		}
		state, err := scope.Update(ctx, nil)
		if err != nil {
			return nil, err
		}
		owned, cancel := context.WithCancel(ctx)
		selected := *cfg.GitHub
		return &Store{ctx: owned, cancel: cancel, baseDir: resolved, selected: &selected, scope: scope, actor: state.Session.ID}, nil
	}
	database, err := db.Open(resolved)
	if err != nil {
		return nil, err
	}
	database.SetMaxOpenConns(1)
	return &Store{db: database}, nil
}

// Init creates a new td database at baseDir and returns a notes Store.
// Production Sidecar should call Open; Init is for tests and first-run setups.
func Init(baseDir string) (*Store, error) {
	cfg, err := config.Load(db.ResolveBaseDir(baseDir))
	if err != nil {
		return nil, err
	}
	kind, err := config.Store(cfg)
	if err != nil {
		return nil, err
	}
	if kind == config.StoreGitHub {
		return Open(baseDir)
	}
	database, err := db.Initialize(baseDir)
	if err != nil {
		return nil, err
	}
	database.SetMaxOpenConns(1)
	return &Store{db: database}, nil
}

// Close cancels GitHub requests or releases the SQLite connection.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	if s.cancel != nil {
		s.cancel()
	}
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Create inserts a note.
func (s *Store) Create(title, content string) (*Note, error) {
	if s.selected != nil {
		c, err := s.client()
		if err != nil {
			return nil, err
		}
		n, err := c.CreateNote(s.ctx, title, content, s.actor)
		return fromGitHub(n), err
	}
	n, err := s.db.CreateNote(title, content)
	return fromModel(n), err
}

// Get returns a live (not soft-deleted) note.
func (s *Store) Get(id string) (*Note, error) {
	if s.selected != nil {
		return s.getGitHub(id, false)
	}
	n, err := s.db.GetNote(id)
	return fromModel(n), err
}

// GetAny returns a note including soft-deleted rows.
func (s *Store) GetAny(id string) (*Note, error) {
	if s.selected != nil {
		return s.getGitHub(id, true)
	}
	n, err := s.db.GetNoteIncludingDeleted(id)
	return fromModel(n), err
}

// List returns notes matching opts. Limit <= 0 means unlimited.
func (s *Store) List(opts ListOptions) ([]Note, error) {
	if s.selected != nil {
		c, err := s.client()
		if err != nil {
			return nil, err
		}
		rows, err := c.ListNotes(s.ctx, opts.IncludeDeleted)
		if err != nil {
			return nil, err
		}
		match := issuestore.SQLiteLIKE("%" + opts.Search + "%")
		out := []Note{}
		for i := range rows {
			n := fromGitHub(&rows[i])
			if opts.Pinned != nil && n.Pinned != *opts.Pinned || opts.Archived != nil && n.Archived != *opts.Archived || opts.Search != "" && !match(n.Title) && !match(n.Content) {
				continue
			}
			out = append(out, *n)
		}
		slices.SortFunc(out, func(a, b Note) int {
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
	rows, err := s.db.ListNotes(db.ListNotesOptions{
		Pinned:         opts.Pinned,
		Archived:       opts.Archived,
		IncludeDeleted: opts.IncludeDeleted,
		Search:         opts.Search,
		Limit:          opts.Limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]Note, 0, len(rows))
	for i := range rows {
		if n := fromModel(&rows[i]); n != nil {
			out = append(out, *n)
		}
	}
	return out, nil
}

// Update changes title and content.
func (s *Store) Update(id, title, content string) (*Note, error) {
	if s.selected != nil {
		n, err := s.Get(id)
		if err != nil {
			return nil, err
		}
		return s.UpdateObserved(n, title, content)
	}
	n, err := s.db.UpdateNote(id, title, content)
	return fromModel(n), err
}

// Delete soft-deletes a note.
func (s *Store) Delete(id string) error {
	if s.selected != nil {
		n, err := s.GetAny(id)
		if err != nil {
			return err
		}
		_, err = s.DeleteObserved(n)
		return err
	}
	return s.db.DeleteNote(id)
}

// Restore undeletes a soft-deleted note.
func (s *Store) Restore(id string) (*Note, error) {
	if s.selected != nil {
		n, err := s.GetAny(id)
		if err != nil {
			return nil, err
		}
		return s.RestoreObserved(n)
	}
	n, err := s.db.RestoreNote(id)
	return fromModel(n), err
}

// Pin pins a note.
func (s *Store) Pin(id string) error {
	if s.selected != nil {
		return s.setFlag(id, true, true)
	}
	return s.db.PinNote(id)
}

// Unpin unpins a note.
func (s *Store) Unpin(id string) error {
	if s.selected != nil {
		return s.setFlag(id, true, false)
	}
	return s.db.UnpinNote(id)
}

// Archive archives a note.
func (s *Store) Archive(id string) error {
	if s.selected != nil {
		return s.setFlag(id, false, true)
	}
	return s.db.ArchiveNote(id)
}

// Unarchive unarchives a note.
func (s *Store) Unarchive(id string) error {
	if s.selected != nil {
		return s.setFlag(id, false, false)
	}
	return s.db.UnarchiveNote(id)
}

func (s *Store) client() (*ghstore.Client, error) {
	if err := s.ctx.Err(); err != nil {
		return nil, err
	}
	state, err := s.scope.Update(s.ctx, nil)
	if err != nil {
		return nil, err
	}
	if state.Session.ID != s.actor {
		return nil, fmt.Errorf("notes session changed; reopen the store before making requests")
	}
	return ghstore.Open(s.ctx, s.baseDir, s.selected)
}
func (s *Store) getGitHub(id string, deleted bool) (*Note, error) {
	// Validate before starting any network request.
	if _, err := ghstore.NoteNumber(id); err != nil {
		return nil, err
	}
	c, err := s.client()
	if err != nil {
		return nil, err
	}
	n, err := c.GetNote(s.ctx, id, deleted)
	return fromGitHub(n), err
}
func fromGitHub(n *ghstore.NoteRecord) *Note {
	if n == nil {
		return nil
	}
	result := fromModel(&n.Note)
	result.observed = n
	return result
}
func (s *Store) writeObserved(n *Note, changes ghstore.NoteChanges) (*Note, error) {
	if n == nil || n.observed == nil || n.ID != n.observed.ID {
		return nil, fmt.Errorf("GitHub note requires its original read observation")
	}
	c, err := s.client()
	if err != nil {
		return nil, err
	}
	result, _, err := c.UpdateNoteObserved(s.ctx, n.observed, changes, s.actor)
	return fromGitHub(result), err
}

// UpdateObserved rejects a stale GitHub read instead of refreshing away the
// observation used by an editor. SQLite retains its existing write-lock path.
func (s *Store) UpdateObserved(n *Note, title, content string) (*Note, error) {
	if n == nil {
		return nil, fmt.Errorf("note is required")
	}
	if s.selected == nil {
		return s.Update(n.ID, title, content)
	}
	return s.writeObserved(n, ghstore.NoteChanges{Title: &title, Content: &content})
}
func (s *Store) DeleteObserved(n *Note) (*Note, error) {
	if n == nil {
		return nil, fmt.Errorf("note is required")
	}
	if s.selected == nil {
		if err := s.Delete(n.ID); err != nil {
			return nil, err
		}
		return s.GetAny(n.ID)
	}
	deleted := true
	return s.writeObserved(n, ghstore.NoteChanges{Deleted: &deleted})
}
func (s *Store) RestoreObserved(n *Note) (*Note, error) {
	if n == nil {
		return nil, fmt.Errorf("note is required")
	}
	if s.selected == nil {
		return s.Restore(n.ID)
	}
	deleted := false
	return s.writeObserved(n, ghstore.NoteChanges{Deleted: &deleted})
}
func (s *Store) SetPinnedObserved(n *Note, value bool) (*Note, error) {
	if n == nil {
		return nil, fmt.Errorf("note is required")
	}
	if s.selected == nil {
		var err error
		if value {
			err = s.Pin(n.ID)
		} else {
			err = s.Unpin(n.ID)
		}
		if err != nil {
			return nil, err
		}
		return s.Get(n.ID)
	}
	return s.writeObserved(n, ghstore.NoteChanges{Pinned: &value})
}
func (s *Store) SetArchivedObserved(n *Note, value bool) (*Note, error) {
	if n == nil {
		return nil, fmt.Errorf("note is required")
	}
	if s.selected == nil {
		var err error
		if value {
			err = s.Archive(n.ID)
		} else {
			err = s.Unarchive(n.ID)
		}
		if err != nil {
			return nil, err
		}
		return s.Get(n.ID)
	}
	return s.writeObserved(n, ghstore.NoteChanges{Archived: &value})
}
func (s *Store) setFlag(id string, pinned, value bool) error {
	n, err := s.Get(id)
	if err != nil {
		return err
	}
	if pinned {
		_, err = s.SetPinnedObserved(n, value)
	} else {
		_, err = s.SetArchivedObserved(n, value)
	}
	return err
}

func fromModel(n *models.Note) *Note {
	if n == nil {
		return nil
	}
	out := &Note{
		ID:        n.ID,
		Title:     n.Title,
		Content:   n.Content,
		CreatedAt: n.CreatedAt,
		UpdatedAt: n.UpdatedAt,
		Pinned:    n.Pinned,
		Archived:  n.Archived,
	}
	if n.DeletedAt != nil {
		t := *n.DeletedAt
		out.DeletedAt = &t
	}
	return out
}

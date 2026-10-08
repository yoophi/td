package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/ghcontext"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/session"
)

// SessionStore is the local session/focus boundary. Session liveness is
// device-local; it is not evidence of a remote review or implementation.
type SessionStore interface {
	ListSessions(context.Context) ([]session.Session, error)
	SetFocus(context.Context, *string) (*string, error)
}

type githubIssueGetter interface {
	Get(context.Context, string) (*ghstore.Record, error)
}
type githubSessionStore struct {
	scope     ghcontext.Scope
	sessionID string
	open      func(context.Context) (githubIssueGetter, error)
}

func NewGitHubSessionStore(dir string, selected models.GitHubStoreConfig, scope ghcontext.Scope, sessionID string) SessionStore {
	return &githubSessionStore{scope: scope, sessionID: sessionID, open: func(ctx context.Context) (githubIssueGetter, error) {
		cfg, err := config.Load(dir)
		if err != nil {
			return nil, err
		}
		kind, err := config.Store(cfg)
		if err != nil {
			return nil, err
		}
		if kind != config.StoreGitHub || cfg.GitHub == nil || *cfg.GitHub != selected {
			return nil, fmt.Errorf("configured store changed; restart td serve")
		}
		return ghstore.Open(ctx, dir, &selected)
	}}
}

var errWebSessionChanged = errors.New("web session changed; restart td serve")
var errFocusUnavailable = errors.New("focus target unavailable")

func (s *githubSessionStore) update(ctx context.Context, change func(*ghcontext.State)) error {
	_, err := s.scope.Update(ctx, func(current *ghcontext.State) error {
		if current.Session.ID != s.sessionID {
			return errWebSessionChanged
		}
		if change != nil {
			change(current)
		}
		return nil
	})
	return err
}
func (s *githubSessionStore) ListSessions(ctx context.Context) ([]session.Session, error) {
	if _, err := s.open(ctx); err != nil {
		return nil, err
	}
	if err := s.update(ctx, nil); err != nil {
		return nil, err
	}
	sessions, err := s.scope.List()
	if err != nil {
		return nil, err
	}
	slices.SortFunc(sessions, func(a, b session.Session) int {
		if n := b.LastActivity.Compare(a.LastActivity); n != 0 {
			return n
		}
		return strings.Compare(a.ID, b.ID)
	})
	return sessions, nil
}
func (s *githubSessionStore) SetFocus(ctx context.Context, id *string) (*string, error) {
	client, err := s.open(ctx)
	if err != nil {
		return nil, err
	}
	focus := ""
	if id != nil && *id != "" {
		n, err := ghstore.Number(*id)
		if err != nil {
			return nil, err
		}
		record, err := client.Get(ctx, fmt.Sprintf("gh-%d", n))
		if err != nil {
			if strings.Contains(err.Error(), "HTTP 404") || strings.Contains(err.Error(), " is deleted; restore it before use") {
				return nil, fmt.Errorf("%w: %v", errFocusUnavailable, err)
			}
			return nil, err
		}
		focus = record.ID
	}
	if err := s.update(ctx, func(state *ghcontext.State) { state.Focus = focus }); err != nil {
		return nil, err
	}
	if focus == "" {
		return nil, nil
	}
	return &focus, nil
}

func sessionStoreError(w http.ResponseWriter, err error) {
	status, code := http.StatusBadGateway, "store_error"
	if errors.Is(err, errWebSessionChanged) {
		status, code = http.StatusConflict, ErrConflict
	}
	if errors.Is(err, errFocusUnavailable) {
		status, code = http.StatusNotFound, ErrNotFound
	}
	WriteError(w, code, err.Error(), status)
}
func (s *Server) registerSessionStore(store SessionStore) {
	s.mux.HandleFunc("GET /v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			WriteError(w, ErrValidation, "session listing does not support query parameters", http.StatusBadRequest)
			return
		}
		sessions, err := store.ListSessions(r.Context())
		if err != nil {
			sessionStoreError(w, err)
			return
		}
		WriteSuccess(w, map[string]any{"sessions": SessionsToDTOs(sessions), "current_session_id": s.sessionID, "liveness_scope": "device-local"}, http.StatusOK)
	})
	s.mux.HandleFunc("PUT /v1/focus", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			WriteError(w, ErrValidation, "focus does not support query parameters", http.StatusBadRequest)
			return
		}
		var body FocusBody
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			WriteError(w, ErrValidation, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			WriteError(w, ErrValidation, "expected one JSON object", http.StatusBadRequest)
			return
		}
		if body.IssueID != nil && *body.IssueID != "" {
			if _, err := ghstore.Number(*body.IssueID); err != nil {
				WriteError(w, ErrValidation, err.Error(), http.StatusBadRequest)
				return
			}
		}
		id, err := store.SetFocus(r.Context(), body.IssueID)
		if err != nil {
			sessionStoreError(w, err)
			return
		}
		WriteSuccess(w, map[string]any{"focused_issue_id": id}, http.StatusOK)
	})
}

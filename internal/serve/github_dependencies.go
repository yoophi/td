package serve

import (
	"context"
	"errors"
	"github.com/marcus/td/internal/db"
	"github.com/marcus/td/internal/ghstore"
	"io"
	"net/http"
	"strings"
)

type githubDependencyClient interface {
	ChangeDependencyObserved(context.Context, *ghstore.Record, string, bool, string) (*ghstore.Record, error)
}

func (s *Server) EnableGitHubDependencies(store *GitHubWriteStore) {
	s.githubCapabilities = append(s.githubCapabilities, "dependencies")
	s.githubEndpoints = append(s.githubEndpoints, "POST /v1/issues/{id}/dependencies", "DELETE /v1/issues/{id}/dependencies/{dep_id}")
	s.mux.HandleFunc("POST /v1/issues/{id}/dependencies", func(w http.ResponseWriter, r *http.Request) { store.dependency(w, r, true) })
	s.mux.HandleFunc("DELETE /v1/issues/{id}/dependencies/{dep_id}", func(w http.ResponseWriter, r *http.Request) { store.dependency(w, r, false) })
}
func (s *GitHubWriteStore) dependency(w http.ResponseWriter, r *http.Request, add bool) {
	target := ""
	if add {
		var body DependencyCreateBody
		if !decodeGitHubWrite(w, r, &body) {
			return
		}
		if strings.TrimSpace(body.DependsOn) == "" {
			WriteValidation(w, []FieldError{{Field: "depends_on", Rule: "required", Message: "depends_on is required"}})
			return
		}
		var err error
		target, err = canonicalParent(body.DependsOn)
		if err != nil {
			WriteError(w, ErrValidation, err.Error(), 400)
			return
		}
	} else {
		if r.Body == nil || r.ContentLength == 0 {
			r.Body = io.NopCloser(strings.NewReader("{}"))
		}
		var body struct{}
		if !decodeGitHubWrite(w, r, &body) {
			return
		}
	}
	if _, err := ghstore.Number(r.PathValue("id")); err != nil {
		WriteError(w, ErrValidation, err.Error(), 400)
		return
	}
	writer, err := s.open(r.Context())
	if err != nil {
		githubWriteError(w, err)
		return
	}
	client, ok := writer.(githubDependencyClient)
	if !ok {
		WriteError(w, "unsupported_operation", "dependency store is unavailable", 501)
		return
	}
	observed, err := writer.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		githubWriteError(w, err)
		return
	}
	if !checkIssueRevision(w, r, &observed.Issue) {
		return
	}
	if !add {
		if observed.Details != nil {
			for _, id := range observed.Details.Dependencies {
				if db.DependencyID(observed.ID, id, "depends_on") == r.PathValue("dep_id") {
					target = id
					break
				}
			}
		}
		if target == "" {
			WriteError(w, ErrNotFound, "dependency not found on issue "+observed.ID, 404)
			return
		}
	}
	if _, err := client.ChangeDependencyObserved(r.Context(), observed, target, add, s.sessionID); err != nil {
		var missing *ghstore.DependencyNotFoundError
		if errors.As(err, &missing) {
			WriteError(w, ErrNotFound, err.Error(), 404)
		} else {
			githubWriteError(w, err)
		}
		return
	}
	if add {
		dto := DependencyDTO{DepID: db.DependencyID(observed.ID, target, "depends_on"), IssueID: observed.ID, DependsOnID: target, RelationType: "depends_on"}
		WriteSuccess(w, map[string]any{"dependency": dto}, http.StatusCreated)
	} else {
		WriteSuccess(w, map[string]any{"removed": true}, http.StatusOK)
	}
}

package serve

import (
	"context"
	"github.com/marcus/td/internal/ghstore"
	"io"
	"net/http"
	"strings"
)

type githubDeletionClient interface {
	GetIncludingDeleted(context.Context, string) (*ghstore.Record, error)
	SetDeletedObserved(context.Context, *ghstore.Record, bool, string, string) (*ghstore.Record, bool, error)
}

func (s *Server) EnableGitHubDeletion(store *GitHubWriteStore) {
	s.githubCapabilities = append(s.githubCapabilities, "logical_delete")
	s.githubEndpoints = append(s.githubEndpoints, "DELETE /v1/issues/{id}")
	s.mux.HandleFunc("DELETE /v1/issues/{id}", store.deleteIssue)
}

func (s *GitHubWriteStore) deleteIssue(w http.ResponseWriter, r *http.Request) {
	if r.Body == nil || r.ContentLength == 0 {
		r.Body = io.NopCloser(strings.NewReader("{}"))
	}
	var body struct{}
	if !decodeGitHubWrite(w, r, &body) {
		return
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
	client, ok := writer.(githubDeletionClient)
	if !ok {
		WriteError(w, "unsupported_operation", "deletion store is unavailable", 501)
		return
	}
	observed, err := client.GetIncludingDeleted(r.Context(), r.PathValue("id"))
	if err != nil {
		githubWriteError(w, err)
		return
	}
	if !checkIssueRevision(w, r, &observed.Issue) {
		return
	}
	if _, _, err := client.SetDeletedObserved(r.Context(), observed, true, s.sessionID, ""); err != nil {
		githubWriteError(w, err)
		return
	}
	WriteSuccess(w, map[string]any{"deleted": true}, http.StatusOK)
}

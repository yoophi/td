package serve

import (
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/query"
)

type githubBoardCRUDClient interface {
	GetBoard(context.Context, string) (*ghstore.BoardRecord, error)
	CreateBoard(context.Context, string, string, string) (*ghstore.BoardRecord, error)
	UpdateBoardObserved(context.Context, *ghstore.BoardRecord, ghstore.BoardChanges, string) (*ghstore.BoardRecord, error)
	DeleteBoardObserved(context.Context, *ghstore.BoardRecord, string, string) (*ghstore.BoardRecord, error)
}

func (s *Server) EnableGitHubBoardWrites(store *GitHubWriteStore) {
	s.githubCapabilities = append(s.githubCapabilities, "board_crud")
	s.githubEndpoints = append(s.githubEndpoints, "POST /v1/boards", "PATCH /v1/boards/{id}", "DELETE /v1/boards/{id}")
	s.mux.HandleFunc("POST /v1/boards", store.createBoard)
	s.mux.HandleFunc("PATCH /v1/boards/{id}", store.updateBoard)
	s.mux.HandleFunc("DELETE /v1/boards/{id}", store.deleteBoard)
}
func validBoardQuery(w http.ResponseWriter, expression string) bool {
	if expression == "" {
		return true
	}
	parsed, err := query.Parse(expression)
	if err != nil || len(parsed.Validate()) > 0 {
		WriteError(w, ErrValidation, "invalid board TDQ query", 400)
		return false
	}
	return true
}
func (s *GitHubWriteStore) openBoardWriter(w http.ResponseWriter, r *http.Request) (githubBoardCRUDClient, bool) {
	raw, err := s.open(r.Context())
	if err != nil {
		githubWriteError(w, err)
		return nil, false
	}
	c, ok := raw.(githubBoardCRUDClient)
	if !ok {
		WriteError(w, "unsupported_operation", "board write store unavailable", 501)
	}
	return c, ok
}
func boardResponse(w http.ResponseWriter, b *ghstore.BoardRecord, status int) {
	w.Header().Set("ETag", `"`+b.Revision()+`"`)
	WriteSuccess(w, map[string]any{"board": githubBoardDTO(b), "revision": b.Revision()}, status)
}
func checkBoardRevision(w http.ResponseWriter, r *http.Request, b *ghstore.BoardRecord) bool {
	if len(r.Header.Values("If-Match")) > 1 {
		WriteError(w, ErrConflict, "Supply one board revision in If-Match; no write was attempted.", 409)
		return false
	}
	if expected := r.Header.Get("If-Match"); expected != "" && expected != b.Revision() && expected != `"`+b.Revision()+`"` {
		WriteError(w, ErrConflict, "This board changed; refresh and compare your draft before saving. No write was attempted.", 409)
		return false
	}
	return true
}
func (s *GitHubWriteStore) createBoard(w http.ResponseWriter, r *http.Request) {
	var body BoardCreateBody
	if !decodeGitHubWrite(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		WriteError(w, ErrValidation, "board name is required", 400)
		return
	}
	if !validBoardQuery(w, body.Query) {
		return
	}
	if r.Header.Get("If-Match") != "" {
		WriteError(w, ErrValidation, "If-Match is not supported for board creation", 400)
		return
	}
	c, ok := s.openBoardWriter(w, r)
	if !ok {
		return
	}
	b, err := c.CreateBoard(r.Context(), body.Name, body.Query, s.sessionID)
	if err != nil {
		boardReadError(w, err)
		return
	}
	boardResponse(w, b, 201)
}
func (s *GitHubWriteStore) updateBoard(w http.ResponseWriter, r *http.Request) {
	var body BoardUpdateBody
	if !decodeGitHubWrite(w, r, &body) {
		return
	}
	if body.Name == nil && body.Query == nil {
		WriteError(w, ErrValidation, "board update requires name or query", 400)
		return
	}
	if body.Name != nil && strings.TrimSpace(*body.Name) == "" {
		WriteError(w, ErrValidation, "board name must not be empty", 400)
		return
	}
	if body.Query != nil && !validBoardQuery(w, *body.Query) {
		return
	}
	c, ok := s.openBoardWriter(w, r)
	if !ok {
		return
	}
	b, err := c.GetBoard(r.Context(), r.PathValue("id"))
	if err != nil {
		boardReadError(w, err)
		return
	}
	if !checkBoardRevision(w, r, b) {
		return
	}
	updated, err := c.UpdateBoardObserved(r.Context(), b, ghstore.BoardChanges{Name: body.Name, Query: body.Query}, s.sessionID)
	if err != nil {
		boardReadError(w, err)
		return
	}
	boardResponse(w, updated, 200)
}
func (s *GitHubWriteStore) deleteBoard(w http.ResponseWriter, r *http.Request) {
	if r.Body == nil || r.ContentLength == 0 {
		r.Body = io.NopCloser(strings.NewReader("{}"))
	}
	var body struct{}
	if !decodeGitHubWrite(w, r, &body) {
		return
	}
	c, ok := s.openBoardWriter(w, r)
	if !ok {
		return
	}
	b, err := c.GetBoard(r.Context(), r.PathValue("id"))
	if err != nil {
		boardReadError(w, err)
		return
	}
	if !checkBoardRevision(w, r, b) {
		return
	}
	if _, err = c.DeleteBoardObserved(r.Context(), b, s.sessionID, ""); err != nil {
		boardReadError(w, err)
		return
	}
	WriteSuccess(w, map[string]any{"deleted": true}, 200)
}

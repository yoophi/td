package serve

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/issuestore"
	"github.com/marcus/td/internal/models"
)

type githubBoardPositionClient interface {
	issuestore.GitHubBoardReader
	Get(context.Context, string) (*ghstore.Record, error)
	GetBoard(context.Context, string) (*ghstore.BoardRecord, error)
	MaterializeBuiltinBoardObserved(context.Context, *ghstore.BoardRecord, string) (*ghstore.BoardRecord, error)
	MoveBoardPositionObserved(context.Context, *ghstore.BoardRecord, string, int, string) (*ghstore.BoardRecord, error)
	MoveBoardBeforeObserved(context.Context, *ghstore.BoardRecord, string, string, []models.Issue, string) (*ghstore.BoardRecord, error)
	RemoveBoardPositionObserved(context.Context, *ghstore.BoardRecord, string, string) (*ghstore.BoardRecord, error)
}

func (s *Server) EnableGitHubBoardPositions(store *GitHubWriteStore) {
	s.githubCapabilities = append(s.githubCapabilities, "board_positions", "board_move")
	s.githubEndpoints = append(s.githubEndpoints, "POST /v1/boards/{id}/issues", "POST /v1/boards/{id}/move", "DELETE /v1/boards/{id}/issues/{issue_id}")
	s.mux.HandleFunc("POST /v1/boards/{id}/issues", store.positionBoardIssue)
	s.mux.HandleFunc("POST /v1/boards/{id}/move", store.moveBoardIssue)
	s.mux.HandleFunc("DELETE /v1/boards/{id}/issues/{issue_id}", store.removeBoardIssuePosition)
}
func canonicalBoardTask(w http.ResponseWriter, id string) bool {
	n, err := ghstore.Number(id)
	if err != nil || id != fmt.Sprintf("gh-%d", n) {
		WriteError(w, ErrValidation, "board task ID must be canonical gh-N", 400)
		return false
	}
	return true
}
func (s *GitHubWriteStore) openBoardPositions(w http.ResponseWriter, r *http.Request) (githubBoardPositionClient, *ghstore.BoardRecord, bool) {
	raw, err := s.open(r.Context())
	if err != nil {
		githubWriteError(w, err)
		return nil, nil, false
	}
	c, ok := raw.(githubBoardPositionClient)
	if !ok {
		WriteError(w, "unsupported_operation", "board position store unavailable", 501)
		return nil, nil, false
	}
	b, err := c.GetBoard(r.Context(), r.PathValue("id"))
	if err != nil {
		boardReadError(w, err)
		return nil, nil, false
	}
	if !checkBoardRevision(w, r, b) {
		return nil, nil, false
	}
	return c, b, true
}
func (s *GitHubWriteStore) materializeBoard(ctx context.Context, c githubBoardPositionClient, b *ghstore.BoardRecord) (*ghstore.BoardRecord, bool, error) {
	if b.Number > 0 {
		return b, false, nil
	}
	persisted, err := c.MaterializeBuiltinBoardObserved(ctx, b, s.sessionID)
	return persisted, err == nil, err
}
func boardPositionError(w http.ResponseWriter, err error, created bool) {
	if created {
		err = fmt.Errorf("builtin board carrier was created, but its position write failed; inspect GitHub before retrying: %w", err)
	}
	boardReadError(w, err)
}
func boardPositionResponse(w http.ResponseWriter, b *ghstore.BoardRecord) {
	w.Header().Set("ETag", `"`+b.Revision()+`"`)
	WriteSuccess(w, map[string]any{"positioned": true, "revision": b.Revision()}, 200)
}
func (s *GitHubWriteStore) positionBoardIssue(w http.ResponseWriter, r *http.Request) {
	var body BoardPositionBody
	if !decodeGitHubWrite(w, r, &body) || !canonicalBoardTask(w, body.IssueID) {
		return
	}
	c, b, ok := s.openBoardPositions(w, r)
	if !ok {
		return
	}
	// Validate before materializing a virtual carrier, so invalid task IDs do
	// not leave behind a configuration issue.
	if _, err := c.Get(r.Context(), body.IssueID); err != nil {
		boardReadError(w, err)
		return
	}
	b, created, err := s.materializeBoard(r.Context(), c, b)
	if err != nil {
		boardReadError(w, err)
		return
	}
	updated, err := c.MoveBoardPositionObserved(r.Context(), b, body.IssueID, max(1, body.Position), s.sessionID)
	if err != nil {
		boardPositionError(w, err, created)
		return
	}
	boardPositionResponse(w, updated)
}
func (s *GitHubWriteStore) moveBoardIssue(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	for key, values := range q {
		if key != "include_closed" || len(values) != 1 || !slices.Contains([]string{"true", "false"}, values[0]) {
			WriteError(w, ErrValidation, "invalid move query parameter: "+key, 400)
			return
		}
	}
	// Preserve the strict body decoder while allowing this existing move filter.
	copyRequest := r.Clone(r.Context())
	copyURL := *r.URL
	copyURL.RawQuery = ""
	copyRequest.URL = &copyURL
	var body struct {
		IssueID       string `json:"issue_id"`
		BeforeID      string `json:"before_id"`
		IncludeClosed bool   `json:"include_closed"`
	}
	if !decodeGitHubWrite(w, copyRequest, &body) || !canonicalBoardTask(w, body.IssueID) {
		return
	}
	if body.BeforeID != "" && !canonicalBoardTask(w, body.BeforeID) {
		return
	}
	if body.BeforeID == body.IssueID {
		WriteError(w, ErrValidation, "cannot move a task before itself", 400)
		return
	}
	c, b, ok := s.openBoardPositions(w, r)
	if !ok {
		return
	}
	candidates, err := issuestore.GitHubBoardCandidates(r.Context(), c, b, body.IncludeClosed || q.Get("include_closed") == "true", body.IssueID, body.BeforeID)
	if err != nil {
		boardReadError(w, err)
		return
	}
	for _, id := range []string{body.IssueID, body.BeforeID} {
		if id != "" && !slices.ContainsFunc(candidates, func(i models.Issue) bool { return i.ID == id }) {
			WriteError(w, ErrConflict, "board changed: moved task or anchor no longer matches; refresh before retrying", 409)
			return
		}
	}
	b, created, err := s.materializeBoard(r.Context(), c, b)
	if err != nil {
		boardReadError(w, err)
		return
	}
	updated, err := c.MoveBoardBeforeObserved(r.Context(), b, body.IssueID, body.BeforeID, candidates, s.sessionID)
	if err != nil {
		boardPositionError(w, err, created)
		return
	}
	boardPositionResponse(w, updated)
}
func (s *GitHubWriteStore) removeBoardIssuePosition(w http.ResponseWriter, r *http.Request) {
	if r.Body == nil || r.ContentLength == 0 {
		r.Body = io.NopCloser(strings.NewReader("{}"))
	}
	var body struct{}
	if !decodeGitHubWrite(w, r, &body) || !canonicalBoardTask(w, r.PathValue("issue_id")) {
		return
	}
	c, b, ok := s.openBoardPositions(w, r)
	if !ok {
		return
	}
	if b.Number == 0 {
		WriteError(w, ErrConflict, "virtual board has no saved positions; no write attempted", 409)
		return
	}
	updated, err := c.RemoveBoardPositionObserved(r.Context(), b, r.PathValue("issue_id"), s.sessionID)
	if err != nil {
		boardReadError(w, err)
		return
	}
	w.Header().Set("ETag", `"`+updated.Revision()+`"`)
	WriteSuccess(w, map[string]any{"removed": true, "revision": updated.Revision()}, 200)
}

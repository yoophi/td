package serve

import (
	"context"
	"errors"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
	"io"
	"net/http"
	"strings"
)

type githubCommentClient interface {
	AppendActivity(context.Context, string, models.Activity) (*models.Activity, error)
	DeleteComment(context.Context, string, string) error
}

func (s *Server) EnableGitHubComments(store *GitHubWriteStore) {
	s.githubCapabilities = append(s.githubCapabilities, "comments")
	s.githubEndpoints = append(s.githubEndpoints, "POST /v1/issues/{id}/comments", "DELETE /v1/issues/{id}/comments/{comment_id}")
	s.mux.HandleFunc("POST /v1/issues/{id}/comments", store.addComment)
	s.mux.HandleFunc("DELETE /v1/issues/{id}/comments/{comment_id}", store.deleteComment)
}
func githubCommentError(w http.ResponseWriter, err error) {
	var missing *ghstore.CommentNotFoundError
	if errors.As(err, &missing) {
		WriteError(w, ErrNotFound, err.Error(), 404)
		return
	}
	githubWriteError(w, err)
}
func (s *GitHubWriteStore) commentClient(w http.ResponseWriter, r *http.Request) githubCommentClient {
	if _, err := ghstore.Number(r.PathValue("id")); err != nil {
		WriteError(w, ErrValidation, err.Error(), 400)
		return nil
	}
	writer, err := s.open(r.Context())
	if err != nil {
		githubCommentError(w, err)
		return nil
	}
	client, ok := writer.(githubCommentClient)
	if !ok {
		WriteError(w, "unsupported_operation", "comment store is unavailable", 501)
		return nil
	}
	return client
}
func (s *GitHubWriteStore) addComment(w http.ResponseWriter, r *http.Request) {
	var body CommentCreateBody
	if !decodeGitHubWrite(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Text) == "" {
		WriteValidation(w, []FieldError{{Field: "text", Rule: "required", Message: "text is required"}})
		return
	}
	if strings.Contains(body.Text, "<!-- td:activity:") {
		WriteError(w, ErrValidation, "comment text contains reserved td activity metadata marker", 400)
		return
	}
	client := s.commentClient(w, r)
	if client == nil {
		return
	}
	activity, err := client.AppendActivity(r.Context(), r.PathValue("id"), models.Activity{Kind: "comment", Message: body.Text, SessionID: s.sessionID})
	if err != nil {
		githubCommentError(w, err)
		return
	}
	comment := models.Comment{ID: activity.ID, IssueID: activity.IssueID, SessionID: activity.SessionID, Text: activity.Message, CreatedAt: activity.CreatedAt}
	WriteSuccess(w, map[string]any{"comment": CommentToDTO(&comment)}, http.StatusCreated)
}
func (s *GitHubWriteStore) deleteComment(w http.ResponseWriter, r *http.Request) {
	if r.Body == nil || r.ContentLength == 0 {
		r.Body = io.NopCloser(strings.NewReader("{}"))
	}
	var body struct{}
	if !decodeGitHubWrite(w, r, &body) {
		return
	}
	if _, err := ghstore.CommentNumber(r.PathValue("comment_id")); err != nil {
		githubCommentError(w, err)
		return
	}
	client := s.commentClient(w, r)
	if client == nil {
		return
	}
	if err := client.DeleteComment(r.Context(), r.PathValue("id"), r.PathValue("comment_id")); err != nil {
		githubCommentError(w, err)
		return
	}
	WriteSuccess(w, map[string]any{"deleted": true}, http.StatusOK)
}

package serve

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

type githubCommentFixture struct {
	githubWriteFixture
	input              models.Activity
	issueID, commentID string
}

func (f *githubCommentFixture) AppendActivity(_ context.Context, id string, a models.Activity) (*models.Activity, error) {
	f.writes++
	f.input = a
	f.issueID = id
	a.ID = "ghc-12"
	a.IssueID = "gh-1"
	a.CreatedAt = time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	return &a, f.failure
}
func (f *githubCommentFixture) DeleteComment(_ context.Context, id, comment string) error {
	f.writes++
	f.issueID = id
	f.commentID = comment
	return f.failure
}

func TestGitHubCommentHTTPContractAndValidation(t *testing.T) {
	f := &githubCommentFixture{}
	store := &GitHubWriteStore{baseDir: t.TempDir(), sessionID: "fixture-web", open: func(context.Context) (githubIssueWriter, error) { return f, nil }}
	s := NewGitHubServer(store.baseDir, store.sessionID, "owner/repo", ServeConfig{})
	s.EnableGitHubComments(store)
	metadata := httptest.NewRecorder()
	s.Handler().ServeHTTP(metadata, httptest.NewRequest("GET", "/v1/project", nil))
	var project struct {
		Data struct {
			Capabilities []string `json:"capabilities"`
			Endpoints    []string `json:"supported_endpoints"`
		} `json:"data"`
	}
	if err := json.Unmarshal(metadata.Body.Bytes(), &project); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(project.Data.Capabilities, "comments") || !slices.Contains(project.Data.Endpoints, "POST /v1/issues/{id}/comments") || !slices.Contains(project.Data.Endpoints, "DELETE /v1/issues/{id}/comments/{comment_id}") {
		t.Fatalf("project support matrix missing: %s", metadata.Body.String())
	}

	request := func(method, path, body string, want int) map[string]any {
		t.Helper()
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		if w.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		var envelope map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		return envelope
	}
	for _, body := range []string{`{}`, `{"text":" "}`, `{"text":"<!-- td:activity:v1"}`, `{"text":"Fixture","session_id":"forged"}`, `{"text":"Fixture"} {}`} {
		request("POST", "/v1/issues/gh-1/comments", body, 400)
	}
	request("POST", "/v1/issues/gh-1/comments?force=true", `{"text":"Fixture"}`, 400)
	request("POST", "/v1/issues/invalid/comments", `{"text":"Fixture"}`, 400)
	request("DELETE", "/v1/issues/gh-1/comments/invalid", "", 400)
	request("DELETE", "/v1/issues/gh-1/comments/ghc-12", `{"force":true}`, 400)
	if f.writes != 0 {
		t.Fatal("invalid request mutated")
	}
	response := request("POST", "/v1/issues/gh-1/comments", `{"text":"Fixture 한글\nline"}`, 201)
	dto := response["data"].(map[string]any)["comment"].(map[string]any)
	if f.input.Kind != "comment" || f.input.SessionID != "fixture-web" || f.input.Message != "Fixture 한글\nline" || dto["id"] != "ghc-12" || dto["issue_id"] != "gh-1" || dto["session_id"] != "fixture-web" || dto["text"] != f.input.Message || dto["created_at"] != "2026-10-09T00:00:00Z" {
		t.Fatalf("context/DTO lost: %+v %v", f.input, dto)
	}
	response = request("DELETE", "/v1/issues/gh-1/comments/ghc-12", "", 200)
	if response["data"].(map[string]any)["deleted"] != true || f.issueID != "gh-1" || f.commentID != "ghc-12" {
		t.Fatal("delete contract lost")
	}
	for _, tc := range []struct {
		err    error
		status int
	}{{&ghstore.CommentNotFoundError{CommentID: "ghc-12", IssueID: "gh-1"}, 404}, {&ghstore.PolicyError{Reason: "protected handoff"}, 403}, {&ghstore.WorkflowStateError{Reason: "changed comment"}, 409}, {errors.New("fixture backend denied"), 502}} {
		f.failure = tc.err
		request("DELETE", "/v1/issues/gh-1/comments/ghc-12", "", tc.status)
	}
	f.failure = errors.New("fixture uncertain activity outcome")
	request("POST", "/v1/issues/gh-1/comments", `{"text":"Fixture"}`, 502)
}

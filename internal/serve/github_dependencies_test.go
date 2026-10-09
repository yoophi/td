package serve

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/marcus/td/internal/db"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

type githubDependencyFixture struct {
	githubWriteFixture
	target, actor string
	add           bool
	observation   *ghstore.Record
}

func (f *githubDependencyFixture) ChangeDependencyObserved(_ context.Context, observed *ghstore.Record, target string, add bool, actor string) (*ghstore.Record, error) {
	f.writes++
	f.target = target
	f.actor = actor
	f.add = add
	f.observation = observed
	return observed, f.failure
}
func TestGitHubDependencyHTTPContractAndRevisionGuard(t *testing.T) {
	f := &githubDependencyFixture{githubWriteFixture: githubWriteFixture{record: ghstore.Record{Issue: models.Issue{ID: "gh-1", Title: "Dependency fixture", Status: models.StatusOpen}, Details: &ghstore.IssueDetails{Dependencies: []string{"gh-2"}}}}}
	store := &GitHubWriteStore{baseDir: t.TempDir(), sessionID: "fixture-web", open: func(context.Context) (githubIssueWriter, error) { return f, nil }}
	s := NewGitHubServer(store.baseDir, store.sessionID, "owner/repo", ServeConfig{})
	s.EnableGitHubDependencies(store)
	request := func(method, path, body, revision string, want int) map[string]any {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if revision != "" {
			r.Header.Set("If-Match", revision)
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		var response map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	for _, body := range []string{`{}`, `{"depends_on":" "}`, `{"depends_on":"invalid"}`, `{"depends_on":"2","session_id":"forged"}`, `{"depends_on":"2","relation_type":"relates_to"}`, `{"depends_on":"2"} {}`} {
		request("POST", "/v1/issues/gh-1/dependencies", body, "", 400)
	}
	request("POST", "/v1/issues/gh-1/dependencies?force=true", `{"depends_on":"2"}`, "", 400)
	request("POST", "/v1/issues/invalid/dependencies", `{"depends_on":"2"}`, "", 400)
	request("POST", "/v1/issues/gh-1/dependencies", `{"depends_on":"2"}`, "stale", 409)
	depID := db.DependencyID("gh-1", "gh-2", "depends_on")
	request("DELETE", "/v1/issues/gh-1/dependencies/"+depID, "", "stale", 409)
	request("DELETE", "/v1/issues/gh-1/dependencies/"+db.DependencyID("gh-9", "gh-2", "depends_on"), "", "", 404)
	request("DELETE", "/v1/issues/gh-1/dependencies/"+depID, `{"force":true}`, "", 400)
	if f.writes != 0 {
		t.Fatal("invalid/stale/foreign edge request mutated")
	}
	response := request("POST", "/v1/issues/1/dependencies", `{"depends_on":"#2"}`, issueRevision(&f.record.Issue), 201)
	dto := response["data"].(map[string]any)["dependency"].(map[string]any)
	if dto["dep_id"] != depID || dto["issue_id"] != "gh-1" || dto["depends_on_id"] != "gh-2" || dto["relation_type"] != "depends_on" || f.actor != "fixture-web" || !f.add || f.target != "gh-2" || f.observation.ID != "gh-1" {
		t.Fatalf("DTO or policy context: %v %+v", dto, f)
	}
	response = request("DELETE", "/v1/issues/gh-1/dependencies/"+depID, "", "", 200)
	if response["data"].(map[string]any)["removed"] != true || f.add || f.target != "gh-2" {
		t.Fatal("remove contract lost")
	}
	for _, tc := range []struct {
		err    error
		status int
	}{{&ghstore.WorkflowInputError{Reason: "circular dependency"}, 400}, {&ghstore.WorkflowStateError{Reason: "dependency already exists"}, 409}, {&ghstore.DependencyNotFoundError{IssueID: "gh-1", DependsOnID: "gh-2"}, 404}, {&ghstore.ConflictError{ID: "gh-1", AfterWrite: true}, 409}, {errors.New("fixture permission failure"), 502}} {
		f.failure = tc.err
		request("POST", "/v1/issues/gh-1/dependencies", `{"depends_on":"2"}`, "", tc.status)
	}
	response = request("GET", "/v1/project", "", "", 200)
	metadata := response["data"].(map[string]any)
	endpoints := []string{}
	for _, item := range metadata["supported_endpoints"].([]any) {
		endpoints = append(endpoints, item.(string))
	}
	caps := []string{}
	for _, item := range metadata["capabilities"].([]any) {
		caps = append(caps, item.(string))
	}
	if !slices.Contains(caps, "dependencies") || !slices.Contains(endpoints, "POST /v1/issues/{id}/dependencies") || !slices.Contains(endpoints, "DELETE /v1/issues/{id}/dependencies/{dep_id}") {
		t.Fatalf("support matrix missing: %v", metadata)
	}
}

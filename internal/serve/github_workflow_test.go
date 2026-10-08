package serve

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
)

type githubWorkflowFixture struct {
	githubWriteFixture
	records    []ghstore.Record
	options    ghstore.TransitionOptions
	action     string
	calls      int
	afterChild bool
}

func (f *githubWorkflowFixture) List(context.Context, bool) ([]ghstore.Record, error) {
	if f.afterChild && f.calls > 0 {
		return []ghstore.Record{{Issue: models.Issue{ID: "gh-2", ParentID: "gh-1"}}}, nil
	}
	return f.records, nil
}
func (f *githubWorkflowFixture) TransitionObservedWithCascades(_ context.Context, r *ghstore.Record, action string, o ghstore.TransitionOptions) (*ghstore.Record, bool, error) {
	f.calls++
	f.action = action
	f.options = o
	if f.failure != nil {
		return nil, false, f.failure
	}
	result := *r
	result.Status = models.StatusInReview
	return &result, false, nil
}
func TestGitHubHTTPWorkflowPolicyWiringAndCascadeBoundary(t *testing.T) {
	f := &githubWorkflowFixture{githubWriteFixture: githubWriteFixture{record: ghstore.Record{Issue: models.Issue{ID: "gh-1", Title: "Workflow fixture", Status: models.StatusOpen}}}}
	store := &GitHubWriteStore{baseDir: t.TempDir(), sessionID: "actual-web-fixture", open: func(context.Context) (githubIssueWriter, error) { return f, nil }}
	srv := NewGitHubServer(store.baseDir, store.sessionID, "owner/repo", ServeConfig{})
	srv.EnableGitHubWorkflow(store)
	request := func(action, body, revision string, want int) {
		t.Helper()
		r := httptest.NewRequest("POST", "/v1/issues/gh-1/"+action, strings.NewReader(body))
		if revision != "" {
			r.Header.Set("If-Match", revision)
		}
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s: %d %s", action, w.Code, w.Body.String())
		}
	}
	request("start", "", "", 200)
	if f.options.SessionID != store.sessionID || f.action != "start" {
		t.Fatalf("%+v", f.options)
	}
	before := f.calls
	request("start", `{"session_id":"forged"}`, "", 400)
	request("start", `{"self_review":true,"reason":"forged"}`, "", 400)
	request("review", `{}`, "stale", 409)
	if f.calls != before {
		t.Fatal("invalid or stale request transitioned")
	}
	request("reviews", `{"decision":"approved","summary":"Fixture self-review","self_review":true}`, "", 200)
	if f.action != "approve" || !f.options.RecordOnly || !f.options.SelfReview || f.options.Reason != "Fixture self-review" {
		t.Fatalf("%+v", f.options)
	}
	f.failure = &ghstore.WorkflowInputError{Reason: "closer must supply reason"}
	request("close", `{}`, "", 400)
	f.failure = &ghstore.PolicyError{Reason: "review denied"}
	request("approve", `{}`, "", 403)
	f.failure = &ghstore.WorkflowStateError{Reason: "stale review"}
	request("review", `{}`, "", 409)
	f.failure = &ghstore.ConflictError{ID: "gh-1"}
	request("review", `{}`, "", 409)
	f.failure = nil
	f.record.ParentID = "gh-9"
	request("review", `{}`, "", 200)
	f.record.ParentID = ""
	f.records = []ghstore.Record{{Issue: models.Issue{ID: "gh-2", ParentID: "gh-1"}}}
	request("review", `{}`, "", 200)
	f.records = []ghstore.Record{{Issue: models.Issue{ID: "gh-2"}, Details: &ghstore.IssueDetails{Dependencies: []string{"gh-1"}}}}
	request("approve", `{}`, "", 200)
	f.records = nil
	f.calls = 0
	f.afterChild = true
	f.failure = &ghstore.ConflictError{ID: "gh-2", AfterWrite: true}
	request("review", `{}`, "", 409)
	if f.calls != 1 {
		t.Fatal("partial result retried")
	}
}

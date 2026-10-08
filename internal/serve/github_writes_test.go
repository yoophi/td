package serve

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
)

type githubWriteFixture struct {
	record  ghstore.Record
	created *models.Issue
	changes ghstore.Changes
	writes  int
	failure error
}

func (f *githubWriteFixture) Get(context.Context, string) (*ghstore.Record, error) {
	r := f.record
	return &r, nil
}
func (f *githubWriteFixture) Create(_ context.Context, issue *models.Issue) (*ghstore.Record, error) {
	f.writes++
	f.created = issue
	if f.failure != nil {
		return nil, f.failure
	}
	r := ghstore.Record{Issue: *issue}
	r.ID = "gh-99"
	r.Status = models.StatusOpen
	return &r, nil
}
func (f *githubWriteFixture) UpdateObserved(_ context.Context, record *ghstore.Record, change ghstore.Changes) (*ghstore.Record, error) {
	f.writes++
	f.changes = change
	if f.failure != nil {
		return nil, f.failure
	}
	return record, nil
}
func TestGitHubHTTPWriteFieldsAndConflictContract(t *testing.T) {
	f := &githubWriteFixture{record: ghstore.Record{Issue: models.Issue{ID: "gh-1", Title: "Existing fixture title", Status: models.StatusOpen, Type: models.TypeTask, Priority: models.PriorityP2}, Details: &ghstore.IssueDetails{ReviewBasis: "preserved-old-basis", Dependencies: []string{"gh-9"}}}}
	store := &GitHubWriteStore{baseDir: t.TempDir(), sessionID: "fixture-web", branch: "fixture-branch", open: func(context.Context) (githubIssueWriter, error) { return f, nil }}
	srv := NewGitHubServer(store.baseDir, store.sessionID, "owner/repo", ServeConfig{})
	srv.EnableGitHubWrites(store)
	request := func(method, path, body, revision string, want int) map[string]any {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if revision != "" {
			r.Header.Set("If-Match", revision)
		}
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		var env map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
			t.Fatal(err)
		}
		return env
	}
	request("POST", "/v1/issues", `{"title":"Created HTTP fixture","type":"bug","priority":"p1","parent_id":"7","sprint":"s1","minor":true,"due_date":"2026-11-01","defer_until":"2026-10-10"}`, "", 201)
	if f.created.ParentID != "gh-7" || f.created.CreatorSession != store.sessionID || f.created.CreatedBranch != store.branch || f.created.Priority != models.PriorityP1 || f.created.DueDate == nil {
		t.Fatalf("%+v", f.created)
	}
	before := f.writes
	request("PATCH", "/v1/issues/gh-1", `{"title":"Updated HTTP fixture"}`, "stale", 409)
	request("PATCH", "/v1/issues/gh-1", `{"status":"closed"}`, "", 400)
	request("PATCH", "/v1/issues/gh-1", `{"due_date":"2026-99-99"}`, "", 400)
	request("POST", "/v1/issues", `{"title":"Created HTTP fixture","unknown":1}`, "", 400)
	request("POST", "/v1/issues", `{"title":"Created HTTP fixture"} {}`, "", 400)
	if f.writes != before {
		t.Fatal("invalid or stale request wrote")
	}
	request("PATCH", "/v1/issues/gh-1", `{"title":"Updated HTTP fixture","parent_id":"","labels":[],"minor":true,"sprint":"s2","due_date":"","defer_until":"2026-12-01"}`, `"`+issueRevision(&f.record.Issue)+`"`, 200)
	d := f.changes.Details
	if d == nil || d.ParentID != "" || !d.Minor || d.DueDate != nil || d.DeferUntil == nil || d.ReviewBasis != "preserved-old-basis" || len(d.Dependencies) != 1 || f.changes.Labels == nil || len(*f.changes.Labels) != 0 {
		t.Fatalf("%+v", f.changes)
	}
	if f.record.Details.Minor {
		t.Fatal("observed record mutated before conflict check")
	}
	for _, after := range []bool{false, true} {
		f.failure = &ghstore.ConflictError{ID: "gh-1", AfterWrite: after}
		request("PATCH", "/v1/issues/gh-1", `{"points":3}`, "", 409)
	}
	f.failure = errors.New("write may have reached GitHub; inspect before retrying")
	before = f.writes
	request("POST", "/v1/issues", `{"title":"Uncertain HTTP fixture"}`, "", 502)
	if f.writes != before+1 {
		t.Fatal("uncertain create retried")
	}
	store.open = func(context.Context) (githubIssueWriter, error) { return nil, errWebSessionChanged }
	request("PATCH", "/v1/issues/gh-1", `{"points":3}`, "", 409)
}

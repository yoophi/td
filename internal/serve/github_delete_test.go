package serve

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
	"net/http/httptest"
	"strings"
	"testing"
)

type githubDeleteFixture struct {
	githubWriteFixture
	actor    string
	observed *ghstore.Record
}

func (f *githubDeleteFixture) GetIncludingDeleted(context.Context, string) (*ghstore.Record, error) {
	r := f.record
	return &r, nil
}
func (f *githubDeleteFixture) SetDeletedObserved(_ context.Context, r *ghstore.Record, deleted bool, actor, reason string) (*ghstore.Record, bool, error) {
	f.writes++
	f.actor = actor
	f.observed = r
	if !deleted {
		panic("HTTP attempted restore")
	}
	return r, false, f.failure
}
func TestGitHubDeleteHTTPRevisionIdentityAndErrorContract(t *testing.T) {
	f := &githubDeleteFixture{githubWriteFixture: githubWriteFixture{record: ghstore.Record{Issue: models.Issue{ID: "gh-1", Title: "Fixture", Status: models.StatusInProgress, ImplementerSession: "fixture-worker"}}}}
	store := &GitHubWriteStore{baseDir: t.TempDir(), sessionID: "fixture-web", open: func(context.Context) (githubIssueWriter, error) { return f, nil }}
	srv := NewGitHubServer(store.baseDir, store.sessionID, "owner/repo", ServeConfig{})
	srv.EnableGitHubDeletion(store)
	request := func(path, body, revision string, want int) {
		t.Helper()
		r := httptest.NewRequest("DELETE", path, strings.NewReader(body))
		if revision != "" {
			r.Header.Set("If-Match", revision)
		}
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		if want == 200 {
			var envelope struct {
				Data struct {
					Deleted bool `json:"deleted"`
				} `json:"data"`
			}
			if json.Unmarshal(w.Body.Bytes(), &envelope) != nil || !envelope.Data.Deleted {
				t.Fatalf("delete response: %s", w.Body.String())
			}
		}
	}
	request("/v1/issues/gh-1", "", "stale", 409)
	request("/v1/issues/gh-1", `{"session_id":"forged"}`, "", 400)
	request("/v1/issues/gh-1?force=true", "", "", 400)
	request("/v1/issues/gh-1", "{} {}", "", 400)
	request("/v1/issues/bad", "", "", 400)
	if f.writes != 0 {
		t.Fatal("invalid input mutated")
	}
	request("/v1/issues/gh-1", "", issueRevision(&f.record.Issue), 200)
	if f.actor != "fixture-web" || f.observed.ImplementerSession != "fixture-worker" {
		t.Fatal("lost actual identity or observation")
	}
	for _, tc := range []struct {
		err    error
		status int
	}{{&ghstore.ConflictError{ID: "gh-1"}, 409}, {&ghstore.ConflictError{ID: "gh-1", AfterWrite: true}, 409}, {errors.New("fixture permission denied"), 502}} {
		f.failure = tc.err
		request("/v1/issues/gh-1", "{}", "", tc.status)
	}
}

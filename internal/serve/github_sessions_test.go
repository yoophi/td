package serve

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marcus/td/internal/ghcontext"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
)

type fixtureIssueGetter struct{ err error }

func (f fixtureIssueGetter) Get(ctx context.Context, id string) (*ghstore.Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.err != nil {
		return nil, f.err
	}
	return &ghstore.Record{Issue: models.Issue{ID: id}}, nil
}
func TestGitHubSessionFocusIsolationAndIdentityConflict(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	web := ghcontext.Scope{Directory: dir, Path: filepath.Join(dir, "web.json")}
	cli := ghcontext.Scope{Directory: dir, Path: filepath.Join(dir, "cli.json")}
	original, err := web.Update(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cli.Update(ctx, func(s *ghcontext.State) error { s.Focus = "gh-99"; return nil }); err != nil {
		t.Fatal(err)
	}
	store := &githubSessionStore{scope: web, sessionID: original.Session.ID, open: func(context.Context) (githubIssueGetter, error) { return fixtureIssueGetter{}, nil }}
	srv := NewGitHubServer(t.TempDir(), original.Session.ID, "owner/repo", ServeConfig{}, store)
	request := func(method, path, body string, want int) map[string]any {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		srv.Handler().ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		var response map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	data := request("GET", "/v1/sessions", "", 200)["data"].(map[string]any)
	if data["current_session_id"] != original.Session.ID || len(data["sessions"].([]any)) != 2 {
		t.Fatalf("%v", data)
	}
	data = request("PUT", "/v1/focus", `{"issue_id":"7"}`, 200)["data"].(map[string]any)
	if data["focused_issue_id"] != "gh-7" {
		t.Fatalf("%v", data)
	}
	request("PUT", "/v1/focus", `{"issue_id":"td-invalid"}`, 400)
	request("PUT", "/v1/focus", `bad json`, 400)
	request("PUT", "/v1/focus", `{"unknown":true}`, 400)
	request("PUT", "/v1/focus", `{} {}`, 400)
	request("GET", "/v1/sessions?unknown=true", "", 400)
	snapshot, err := os.ReadFile(web.Path)
	if err != nil {
		t.Fatal(err)
	}
	store.open = func(context.Context) (githubIssueGetter, error) {
		return fixtureIssueGetter{err: errors.New("issue gh-8 is deleted; restore it before use")}, nil
	}
	request("PUT", "/v1/focus", `{"issue_id":"8"}`, 404)
	after, _ := os.ReadFile(web.Path)
	if string(after) != string(snapshot) {
		t.Fatal("failed target lookup changed focus")
	}
	request("PUT", "/v1/focus", `{"issue_id":null}`, 200)
	state, err := cli.Update(ctx, nil)
	if err != nil || state.Focus != "gh-99" {
		t.Fatalf("CLI focus overwritten: %+v %v", state, err)
	}
	store.open = func(context.Context) (githubIssueGetter, error) { return nil, errors.New("configured store changed") }
	request("PUT", "/v1/focus", `{}`, 502)
	store.open = func(context.Context) (githubIssueGetter, error) { return fixtureIssueGetter{}, nil }
	_, err = web.Update(ctx, func(s *ghcontext.State) error { web.NewSession(s); return nil })
	if err != nil {
		t.Fatal(err)
	}
	request("GET", "/v1/sessions", "", 409)
	request("PUT", "/v1/focus", `{"issue_id":"7"}`, 409)
}

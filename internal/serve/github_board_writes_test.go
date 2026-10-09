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

type githubBoardCRUDFixture struct {
	githubWriteFixture
	board        ghstore.BoardRecord
	actor        string
	boardChanges ghstore.BoardChanges
	boardErr     error
}

func (f *githubBoardCRUDFixture) GetBoard(context.Context, string) (*ghstore.BoardRecord, error) {
	if f.boardErr != nil {
		return nil, f.boardErr
	}
	b := f.board
	return &b, nil
}
func (f *githubBoardCRUDFixture) CreateBoard(_ context.Context, name, expression, actor string) (*ghstore.BoardRecord, error) {
	f.writes++
	f.actor = actor
	if f.failure != nil {
		return nil, f.failure
	}
	f.board = ghstore.BoardRecord{Board: models.Board{ID: "bd-gh-4", Name: name, Query: expression, ViewMode: "swimlanes"}, Number: 4, Details: ghstore.BoardDetails{Version: 1, Query: expression, ViewMode: "swimlanes"}}
	return &f.board, nil
}
func (f *githubBoardCRUDFixture) UpdateBoardObserved(_ context.Context, b *ghstore.BoardRecord, ch ghstore.BoardChanges, actor string) (*ghstore.BoardRecord, error) {
	f.writes++
	f.actor = actor
	f.boardChanges = ch
	if f.failure != nil {
		return nil, f.failure
	}
	if b.IsBuiltin {
		return nil, &ghstore.PolicyError{Reason: "cannot modify builtin board"}
	}
	if ch.Name != nil {
		f.board.Name = *ch.Name
	}
	if ch.Query != nil {
		f.board.Query = *ch.Query
	}
	return &f.board, nil
}
func (f *githubBoardCRUDFixture) DeleteBoardObserved(_ context.Context, b *ghstore.BoardRecord, actor, reason string) (*ghstore.BoardRecord, error) {
	f.writes++
	f.actor = actor
	if f.failure != nil {
		return nil, f.failure
	}
	if b.IsBuiltin {
		return nil, &ghstore.PolicyError{Reason: "cannot delete builtin board"}
	}
	return b, nil
}

func TestGitHubBoardCRUDHTTPValidationActorsAndRevision(t *testing.T) {
	f := &githubBoardCRUDFixture{}
	opens := 0
	unsupported := false
	openErr := error(nil)
	store := &GitHubWriteStore{baseDir: t.TempDir(), sessionID: "actual-web", open: func(context.Context) (githubIssueWriter, error) {
		opens++
		if openErr != nil {
			return nil, openErr
		}
		if unsupported {
			return &githubWriteFixture{}, nil
		}
		return f, nil
	}}
	srv := NewGitHubServer(store.baseDir, store.sessionID, "owner/repo", ServeConfig{})
	srv.EnableGitHubBoardWrites(store)
	request := func(method, path, body, revision string, want int) map[string]any {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if revision != "" {
			r.Header.Set("If-Match", revision)
		}
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		var env map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
			t.Fatal(err)
		}
		if want == 201 || want == 200 && method == "PATCH" {
			if w.Header().Get("ETag") == "" {
				t.Fatal("missing revision header")
			}
		}
		return env
	}
	before := opens
	for _, body := range []string{`{}`, `{"name":" "}`, `{"name":"Sprint","query":"status ="}`, `{"name":"Sprint","query":"future_field = x"}`, `{"name":"Sprint","session_id":"forged"}`, `{"name":"Sprint"} {}`} {
		request("POST", "/v1/boards", body, "", 400)
	}
	request("POST", "/v1/boards?query=x", `{"name":"Sprint"}`, "", 400)
	request("POST", "/v1/boards", `{"name":"Sprint"}`, "stale", 400)
	if opens != before || f.writes != 0 {
		t.Fatal("invalid creation opened/wrote")
	}
	env := request("POST", "/v1/boards", `{"name":"Sprint","query":"status = open"}`, "", 201)
	data := env["data"].(map[string]any)
	if f.actor != "actual-web" || data["board"].(map[string]any)["id"] != "bd-gh-4" {
		t.Fatal(data)
	}
	revision := data["revision"].(string)
	before = f.writes
	request("PATCH", "/v1/boards/bd-gh-4", `{"name":"Rename"}`, "stale", 409)
	request("DELETE", "/v1/boards/bd-gh-4", "", "stale", 409)
	if f.writes != before {
		t.Fatal("If-Match conflict wrote")
	}
	repeated := httptest.NewRequest("PATCH", "/v1/boards/bd-gh-4", strings.NewReader(`{"name":"Rename"}`))
	repeated.Header.Add("If-Match", revision)
	repeated.Header.Add("If-Match", "stale")
	recorder := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recorder, repeated)
	if recorder.Code != 409 || f.writes != before {
		t.Fatalf("repeated revision %d %s", recorder.Code, recorder.Body.String())
	}
	opensBefore := opens
	for _, body := range []string{`{}`, `{"name":" "}`, `{"query":"future_field = x"}`, `{"view_mode":"backlog"}`, `{"name":"Rename","session_id":"forged"}`} {
		request("PATCH", "/v1/boards/bd-gh-4", body, "", 400)
	}
	request("DELETE", "/v1/boards/bd-gh-4", `{"reason":"unsupported"}`, "", 400)
	if opens != opensBefore || f.writes != before {
		t.Fatal("invalid update/delete opened store")
	}
	request("PATCH", "/v1/boards/bd-gh-4", `{"name":"Rename","query":""}`, `"`+revision+`"`, 200)
	if f.actor != "actual-web" || f.boardChanges.Query == nil || *f.boardChanges.Query != "" || f.board.Name != "Rename" {
		t.Fatalf("%+v", f)
	}
	request("DELETE", "/v1/boards/bd-gh-4", "", f.board.Revision(), 200)
	if f.actor != "actual-web" || f.created != nil {
		t.Fatal("wrong actor or issue write")
	}
	f.boardErr = errors.New("board not found: missing")
	request("PATCH", "/v1/boards/missing", `{"name":"Rename"}`, "", 404)
	f.boardErr = nil
	unsupported = true
	request("POST", "/v1/boards", `{"name":"Sprint"}`, "", 501)
	unsupported = false
	openErr = errors.New("gh missing")
	request("POST", "/v1/boards", `{"name":"Sprint"}`, "", 502)
	openErr = nil
	f.failure = &ghstore.ConflictError{ID: "bd-gh-4", AfterWrite: true}
	request("PATCH", "/v1/boards/bd-gh-4", `{"name":"Rename"}`, "", 409)
	f.failure = errors.New("write outcome may be unknown; inspect GitHub before retrying")
	request("DELETE", "/v1/boards/bd-gh-4", "", "", 502)
	f.failure = nil
	f.board.IsBuiltin = true
	request("PATCH", "/v1/boards/bd-gh-4", `{"name":"Rename"}`, "", 403)
	request("DELETE", "/v1/boards/bd-gh-4", "", "", 403)
}

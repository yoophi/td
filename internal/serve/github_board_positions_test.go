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

type githubBoardPositionsFixture struct {
	githubBoardCRUDFixture
	records           []ghstore.Record
	issueID, beforeID string
	slot              int
	candidates        []models.Issue
	materializations  int
	materializeErr    error
	gets              int
}

func (f *githubBoardPositionsFixture) Get(_ context.Context, id string) (*ghstore.Record, error) {
	f.gets++
	for _, r := range f.records {
		if r.ID == id {
			return &r, nil
		}
	}
	return nil, errors.New("HTTP 404")
}
func (f *githubBoardPositionsFixture) List(context.Context, bool) ([]ghstore.Record, error) {
	return f.records, nil
}
func (f *githubBoardPositionsFixture) ListActivity(context.Context, string) ([]models.Activity, error) {
	return nil, nil
}
func (f *githubBoardPositionsFixture) AppendActivity(context.Context, string, models.Activity) (*models.Activity, error) {
	panic("position wrote activity")
}
func (f *githubBoardPositionsFixture) BoardQueryObserved(b *ghstore.BoardRecord) (string, error) {
	return b.Query, nil
}
func (f *githubBoardPositionsFixture) MaterializeBuiltinBoardObserved(_ context.Context, b *ghstore.BoardRecord, actor string) (*ghstore.BoardRecord, error) {
	f.materializations++
	if f.materializeErr != nil {
		return nil, f.materializeErr
	}
	f.writes++
	f.actor = actor
	f.board = *b
	f.board.Number = 7
	return &f.board, nil
}
func (f *githubBoardPositionsFixture) MoveBoardPositionObserved(_ context.Context, b *ghstore.BoardRecord, id string, slot int, actor string) (*ghstore.BoardRecord, error) {
	f.writes++
	f.issueID = id
	f.slot = slot
	f.actor = actor
	if f.failure != nil {
		return nil, f.failure
	}
	return b, nil
}
func (f *githubBoardPositionsFixture) MoveBoardBeforeObserved(_ context.Context, b *ghstore.BoardRecord, id, before string, candidates []models.Issue, actor string) (*ghstore.BoardRecord, error) {
	f.writes++
	f.issueID = id
	f.beforeID = before
	f.candidates = candidates
	f.actor = actor
	if f.failure != nil {
		return nil, f.failure
	}
	return b, nil
}
func (f *githubBoardPositionsFixture) RemoveBoardPositionObserved(_ context.Context, b *ghstore.BoardRecord, id, actor string) (*ghstore.BoardRecord, error) {
	f.writes++
	f.issueID = id
	f.actor = actor
	if f.failure != nil {
		return nil, f.failure
	}
	return b, nil
}
func TestGitHubBoardPositionHTTPValidationMovesAndBuiltin(t *testing.T) {
	f := &githubBoardPositionsFixture{githubBoardCRUDFixture: githubBoardCRUDFixture{board: ghstore.BoardRecord{Board: models.Board{ID: "bd-gh-8", Name: "Sprint"}, Number: 8}}, records: []ghstore.Record{
		{Issue: models.Issue{ID: "gh-1", Priority: models.PriorityP0, Status: models.StatusOpen}},
		{Issue: models.Issue{ID: "gh-2", Priority: models.PriorityP1, Status: models.StatusClosed}},
		{Issue: models.Issue{ID: "gh-3", Priority: models.PriorityP2, Status: models.StatusOpen}},
	}}
	opens := 0
	unsupported := false
	store := &GitHubWriteStore{baseDir: t.TempDir(), sessionID: "actual-web", open: func(context.Context) (githubIssueWriter, error) {
		opens++
		if unsupported {
			return &githubWriteFixture{}, nil
		}
		return f, nil
	}}
	srv := NewGitHubServer(store.baseDir, store.sessionID, "owner/repo", ServeConfig{})
	srv.EnableGitHubBoardPositions(store)
	request := func(method, path, body, revision string, want int) string {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if revision != "" {
			r.Header.Set("If-Match", revision)
		}
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s %d %s", method, path, w.Code, w.Body.String())
		}
		if want == 200 {
			var env map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
				t.Fatal(err)
			}
			if w.Header().Get("ETag") == "" {
				t.Fatal("missing revision")
			}
		}
		return w.Body.String()
	}
	for _, body := range []string{`{}`, `{"issue_id":"1"}`, `{"issue_id":"gh-1","session_id":"forged"}`} {
		request("POST", "/v1/boards/bd-gh-8/issues", body, "", 400)
	}
	request("POST", "/v1/boards/bd-gh-8/move", `{"issue_id":"gh-1","before_id":"gh-1"}`, "", 400)
	request("POST", "/v1/boards/bd-gh-8/move?include_closed=1", `{"issue_id":"gh-1"}`, "", 400)
	request("POST", "/v1/boards/bd-gh-8/move?include_closed=true&include_closed=false", `{"issue_id":"gh-1"}`, "", 400)
	if opens != 0 || f.writes != 0 {
		t.Fatal("invalid input opened store")
	}
	request("POST", "/v1/boards/bd-gh-8/issues", `{"issue_id":"gh-1","position":0}`, "stale", 409)
	if f.writes != 0 {
		t.Fatal("stale revision wrote")
	}
	request("POST", "/v1/boards/bd-gh-8/issues", `{"issue_id":"gh-1","position":0}`, "", 200)
	if f.slot != 1 || f.actor != "actual-web" || f.materializations != 0 {
		t.Fatalf("position %+v", f)
	}
	request("POST", "/v1/boards/bd-gh-8/move?include_closed=true", `{"issue_id":"gh-3","before_id":"gh-1"}`, "", 200)
	if f.beforeID != "gh-1" || len(f.candidates) != 3 || f.actor != "actual-web" {
		t.Fatalf("move %+v", f)
	}
	request("POST", "/v1/boards/bd-gh-8/move", `{"issue_id":"gh-2","before_id":"gh-1"}`, "", 200)
	if len(f.candidates) != 3 {
		t.Fatal("closed moved task was lost")
	}
	f.board.Query = "id != gh-3"
	before := f.writes
	request("POST", "/v1/boards/bd-gh-8/move", `{"issue_id":"gh-3","before_id":"gh-1"}`, "", 409)
	if f.writes != before {
		t.Fatal("query mismatch wrote")
	}
	f.board.Query = ""
	gets := f.gets
	request("DELETE", "/v1/boards/bd-gh-8/issues/gh-99", "", "", 200)
	if f.gets != gets || f.issueID != "gh-99" {
		t.Fatal("cleanup required live target")
	}
	f.board = ghstore.BoardRecord{Board: models.Board{ID: "bd-all-issues", Name: "All Issues", IsBuiltin: true}}
	before = f.writes
	request("POST", "/v1/boards/bd-all-issues/issues", `{"issue_id":"gh-99"}`, "virtual", 404)
	request("POST", "/v1/boards/bd-all-issues/move", `{"issue_id":"gh-99"}`, "virtual", 409)
	request("DELETE", "/v1/boards/bd-all-issues/issues/gh-1", "", "virtual", 409)
	if f.writes != before || f.materializations != 0 {
		t.Fatal("invalid virtual operations materialized")
	}
	f.materializeErr = &ghstore.ConflictError{ID: "bd-all-issues"}
	request("POST", "/v1/boards/bd-all-issues/issues", `{"issue_id":"gh-1"}`, "virtual", 409)
	if f.writes != before {
		t.Fatal("materialization conflict wrote")
	}
	f.materializeErr = nil
	f.failure = errors.New("permission denied during position write")
	message := request("POST", "/v1/boards/bd-all-issues/issues", `{"issue_id":"gh-1"}`, "virtual", 502)
	if !strings.Contains(message, "carrier was created") || f.materializations != 2 {
		t.Fatal(message)
	}
	f.failure = nil
	request("POST", "/v1/boards/bd-all-issues/issues", `{"issue_id":"gh-1","position":2}`, "", 200)
	if f.slot != 2 || f.actor != "actual-web" {
		t.Fatalf("builtin %+v", f)
	}
	unsupported = true
	request("POST", "/v1/boards/bd-gh-8/issues", `{"issue_id":"gh-1"}`, "", 501)
}

// Existing HTTP clients gate movement on board_move, rather than the broader
// GitHub-specific board_positions capability.
func TestGitHubBoardMoveCapabilityRequiresRegisteredWriter(t *testing.T) {
	srv := NewGitHubServer(t.TempDir(), "actual-web", "owner/repo", ServeConfig{})
	capabilities := func() []string {
		t.Helper()
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/v1/project", nil))
		var body struct {
			Data struct {
				Capabilities []string `json:"capabilities"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body.Data.Capabilities
	}
	for _, c := range capabilities() {
		if c == "board_move" {
			t.Fatal("unregistered move advertised")
		}
	}
	srv.EnableGitHubBoardPositions(&GitHubWriteStore{})
	found := false
	for _, c := range capabilities() {
		if c == "board_move" {
			found = true
		}
	}
	if !found {
		t.Fatal("existing clients cannot discover supported movement")
	}
}

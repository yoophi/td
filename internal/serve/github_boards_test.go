package serve

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
)

type githubBoardsFixture struct {
	githubReadFixture
	boards   []ghstore.BoardRecord
	query    string
	boardErr error
	lists    int
}

func (f *githubBoardsFixture) ListBoards(context.Context) ([]ghstore.BoardRecord, error) {
	return f.boards, f.boardErr
}
func (f *githubBoardsFixture) GetBoard(_ context.Context, id string) (*ghstore.BoardRecord, error) {
	if f.boardErr != nil {
		return nil, f.boardErr
	}
	for _, b := range f.boards {
		if b.ID == id || b.Name == id {
			return &b, nil
		}
	}
	return nil, errors.New("board not found: " + id)
}
func (f *githubBoardsFixture) BoardQueryObserved(*ghstore.BoardRecord) (string, error) {
	return f.query, nil
}
func (f *githubBoardsFixture) List(ctx context.Context, all bool) ([]ghstore.Record, error) {
	f.lists++
	return f.githubReadFixture.List(ctx, all)
}

func TestGitHubBoardReadHTTPContractsAndFailures(t *testing.T) {
	now := time.Now().UTC()
	f := &githubBoardsFixture{boards: []ghstore.BoardRecord{{Board: models.Board{ID: "bd-gh-8", Name: "Sprint", ViewMode: "backlog"}, Number: 8, Details: ghstore.BoardDetails{Version: 1, ViewMode: "backlog", Positions: []ghstore.BoardPosition{{IssueID: "gh-2", Position: 4, AddedAt: now}}}}}, githubReadFixture: githubReadFixture{records: []ghstore.Record{
		{Issue: models.Issue{ID: "gh-1", Title: "Blocked", Description: "heavy text", Priority: models.PriorityP0, Status: models.StatusOpen}, Details: &ghstore.IssueDetails{Dependencies: []string{"gh-3"}}},
		{Issue: models.Issue{ID: "gh-2", Title: "Positioned", Priority: models.PriorityP2, Status: models.StatusOpen}},
		{Issue: models.Issue{ID: "gh-3", Title: "Outside board", Priority: models.PriorityP1, Status: models.StatusInProgress}},
		{Issue: models.Issue{ID: "gh-4", Title: "Closed", Priority: models.PriorityP1, Status: models.StatusClosed}},
	}}}
	opens := 0
	unsupported := false
	openErr := error(nil)
	srv := NewGitHubServer(t.TempDir(), "actual-web", "owner/repo", ServeConfig{})
	srv.EnableGitHubBoardReads(&GitHubReadStore{open: func(context.Context) (githubReadClient, error) {
		opens++
		if openErr != nil {
			return nil, openErr
		}
		if unsupported {
			return &githubReadFixture{}, nil
		}
		return f, nil
	}})
	request := func(path string, want int) map[string]any {
		t.Helper()
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != want {
			t.Fatalf("%s %d %s", path, w.Code, w.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	body := request("/v1/boards", 200)["data"].(map[string]any)
	if len(body["boards"].([]any)) != 1 {
		t.Fatal(body)
	}
	f.query = "id != gh-3"
	before := f.lists
	data := request("/v1/boards/bd-gh-8", 200)["data"].(map[string]any)
	cards := data["issues"].([]any)
	if len(cards) != 2 || f.lists != before+1 {
		t.Fatalf("cards=%v lists=%d", cards, f.lists)
	}
	first := cards[0].(map[string]any)
	if first["has_position"] != true || first["issue"].(map[string]any)["id"] != "gh-2" {
		t.Fatal(first)
	}
	second := cards[1].(map[string]any)["issue"].(map[string]any)
	if second["description"] != "" {
		t.Fatal("heavy text leaked")
	}
	summary := second["dependency_summary"].(map[string]any)
	if summary["blockers"].([]any)[0].(map[string]any)["issue_id"] != "gh-3" {
		t.Fatal(summary)
	}
	data = request("/v1/boards/Sprint?include_closed=true", 200)["data"].(map[string]any)
	if len(data["issues"].([]any)) != 3 {
		t.Fatal(data)
	}
	before = opens
	for _, path := range []string{"/v1/boards?include_closed=true", "/v1/boards/bd-gh-8?include_closed=1", "/v1/boards/bd-gh-8?include_closed=true&include_closed=false", "/v1/boards/bd-gh-8?sort=priority"} {
		request(path, 400)
	}
	if opens != before {
		t.Fatal("invalid query opened store")
	}
	request("/v1/boards/missing", 404)
	f.query = "id = gh-99"
	data = request("/v1/boards/bd-gh-8", 200)["data"].(map[string]any)
	if len(data["issues"].([]any)) != 0 {
		t.Fatal(data)
	}
	f.query = ""
	f.boards = nil
	data = request("/v1/boards", 200)["data"].(map[string]any)
	if data["boards"] == nil {
		t.Fatal("null boards")
	}
	unsupported = true
	request("/v1/boards", 501)
	unsupported = false
	openErr = errors.New("gh missing")
	request("/v1/boards", 502)
	openErr = nil
	f.boardErr = errors.New("ambiguous board name; reconcile")
	request("/v1/boards/bd-gh-8", 502)
	project := request("/v1/project", 200)["data"].(map[string]any)
	caps := project["capabilities"].([]any)
	if !slices.Contains(caps, any("board_reads")) {
		t.Fatal(project)
	}
}

func TestGitHubBoardReadMissingDependencyAndRateLimit(t *testing.T) {
	f := &githubBoardsFixture{boards: []ghstore.BoardRecord{{Board: models.Board{ID: "bd-all-issues", Name: "All Issues", IsBuiltin: true, ViewMode: "swimlanes"}, Details: ghstore.BoardDetails{Version: 1, Builtin: true, ViewMode: "swimlanes"}}}, githubReadFixture: githubReadFixture{records: []ghstore.Record{{Issue: models.Issue{ID: "gh-1", Priority: models.PriorityP2, Status: models.StatusOpen}, Details: &ghstore.IssueDetails{Dependencies: []string{"gh-9"}}}}}}
	srv := NewGitHubServer(t.TempDir(), "web", "owner/repo", ServeConfig{})
	srv.EnableGitHubBoardReads(&GitHubReadStore{open: func(context.Context) (githubReadClient, error) { return f, nil }})
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/v1/boards/bd-all-issues", nil))
	if w.Code != 502 || !strings.Contains(w.Body.String(), "missing or deleted") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	f.records = nil
	w = httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/v1/boards/bd-all-issues", nil))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var body struct {
		Data struct {
			Board  BoardDTO
			Issues []any
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Data.Board.CreatedAt != "" || body.Data.Board.UpdatedAt != "" || body.Data.Issues == nil {
		t.Fatalf("virtual %+v", body)
	}
	f.boardErr = &ghstore.RateLimitError{Cause: errors.New("HTTP 429 rate limit exceeded"), RetryAt: time.Now().Add(time.Hour), WaitSource: "retry-after"}
	w = httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/v1/boards", nil))
	if w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatalf("rate %d %s", w.Code, w.Body.String())
	}
}

package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
)

type githubReadFixture struct {
	records       []ghstore.Record
	activityCalls int
	fail          bool
}

func (f *githubReadFixture) Get(ctx context.Context, id string) (*ghstore.Record, error) {
	n, err := ghstore.Number(id)
	if err != nil {
		return nil, err
	}
	id = fmt.Sprintf("gh-%d", n)
	for _, r := range f.records {
		if r.ID == id {
			return &r, nil
		}
	}
	return nil, errors.New("HTTP 404")
}
func (f *githubReadFixture) List(ctx context.Context, all bool) ([]ghstore.Record, error) {
	return f.records, nil
}
func (f *githubReadFixture) AppendActivity(context.Context, string, models.Activity) (*models.Activity, error) {
	return nil, errors.New("unexpected write")
}
func (f *githubReadFixture) ListActivity(ctx context.Context, id string) ([]models.Activity, error) {
	f.activityCalls++
	if f.fail {
		return nil, errors.New("fixture permission denied")
	}
	if id != "gh-2" {
		return []models.Activity{}, nil
	}
	return []models.Activity{
		{ID: "ghc-1", IssueID: id, Kind: "log", Message: "authentication fixed", LogType: models.LogTypeProgress, CreatedAt: time.Now()},
		{ID: "ghc-2", IssueID: id, Kind: "comment", Message: "native comment", Native: true},
		{ID: "ghc-3", IssueID: id, Kind: "handoff", Done: []string{"finished"}},
	}, nil
}
func TestGitHubHTTPReadContractsAndTDQ(t *testing.T) {
	f := &githubReadFixture{records: []ghstore.Record{
		{Issue: models.Issue{ID: "gh-1", Title: "Parent", Type: models.TypeEpic, Status: models.StatusOpen, Priority: models.PriorityP1}, Number: 1},
		{Issue: models.Issue{ID: "gh-2", Title: "Auth child", Description: "full description", Type: models.TypeTask, Status: models.StatusInProgress, Priority: models.PriorityP2, ParentID: "gh-1", Labels: []string{"backend"}}, Number: 2, Details: &ghstore.IssueDetails{Dependencies: []string{"gh-1"}}},
		{Issue: models.Issue{ID: "gh-3", Title: "Closed child", Type: models.TypeTask, Status: models.StatusClosed, Priority: models.PriorityP3, ParentID: "gh-1"}, Number: 3},
	}}
	srv := NewGitHubServer(t.TempDir(), "fixture-web", "owner/repo", ServeConfig{})
	srv.EnableGitHubReads(&GitHubReadStore{open: func(context.Context) (githubReadClient, error) { return f, nil }})
	request := func(path string, want int) map[string]any {
		t.Helper()
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != want {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		var env struct{ Data map[string]any }
		if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
			t.Fatal(err)
		}
		return env.Data
	}
	data := request("/v1/issues?limit=1", 200)
	if data["total"] != float64(2) || data["has_more"] != true {
		t.Fatalf("%v", data)
	}
	data = request("/v1/issues?epic=1&include_closed=true&sort=title", 200)
	if data["total"] != float64(2) {
		t.Fatalf("%v", data)
	}
	data = request("/v1/issues?status=in_progress&labels=backend", 200)
	if data["total"] != float64(1) {
		t.Fatalf("%v", data)
	}
	card := data["issues"].([]any)[0].(map[string]any)
	if card["dependency_summary"] == nil {
		t.Fatal("missing blockers")
	}
	for _, q := range []string{`status = in_progress`, `descendant_of(gh-1) AND status != closed`, `has_open_deps()`, `log.message ~ "authentication"`} {
		data = request("/v1/issues?search_mode=tdq&search="+url.QueryEscape(q), 200)
		if data["total"] != float64(1) {
			t.Fatalf("%s: %v", q, data)
		}
	}
	before := f.activityCalls
	data = request("/v1/issues/2?with=reviews", 200)
	if f.activityCalls != before+1 {
		t.Fatal("activity not cached per detail request")
	}
	if data["issue"].(map[string]any)["description"] != "full description" || len(data["logs"].([]any)) != 1 || len(data["comments"].([]any)) != 1 || data["latest_handoff"] == nil || len(data["dependencies"].([]any)) != 1 {
		t.Fatalf("%v", data)
	}
	request("/v1/issues/999", 404)
	request("/v1/issues?limit=-1", 400)
	request("/v1/issues?unknown=1", 400)
	request("/v1/issues/2?with=unknown", 400)
	f.fail = true
	request("/v1/issues/2", 502)
	request("/v1/issues?search_mode=tdq&search="+url.QueryEscape(`log.message ~ "authentication"`), 502)
	f.fail = false
	f.records = append(f.records, f.records[0])
	request("/v1/issues", 502)
}

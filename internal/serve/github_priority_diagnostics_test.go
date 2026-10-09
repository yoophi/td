package serve

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
)

type priorityDiagnosticFixture struct {
	githubBoardsFixture
	gets int
}

func (f *priorityDiagnosticFixture) Get(ctx context.Context, id string) (*ghstore.Record, error) {
	f.gets++
	return f.githubBoardsFixture.Get(ctx, id)
}

func TestGitHubPriorityWarningListBoardAndDetailWithoutExtraReads(t *testing.T) {
	warning := "gh-1: priority labels differ from authoritative metadata P1"
	f := &priorityDiagnosticFixture{githubBoardsFixture: githubBoardsFixture{
		boards:            []ghstore.BoardRecord{{Board: models.Board{ID: "bd-all-issues", Name: "All Issues", IsBuiltin: true, ViewMode: "swimlanes"}, Details: ghstore.BoardDetails{Version: 1, Builtin: true, ViewMode: "swimlanes"}}},
		githubReadFixture: githubReadFixture{records: []ghstore.Record{{Issue: models.Issue{ID: "gh-1", Title: "Legacy priority", Priority: models.PriorityP1, Status: models.StatusOpen, Type: models.TypeTask, Labels: []string{"td:priority:P4"}}, PriorityLabelDiagnostic: warning}}},
	}}
	srv := NewGitHubServer(t.TempDir(), "fixture-web", "owner/repo", ServeConfig{})
	store := &GitHubReadStore{open: func(context.Context) (githubReadClient, error) { return f, nil }}
	srv.EnableGitHubReads(store)
	srv.EnableGitHubBoardReads(store)
	request := func(path string) map[string]any {
		t.Helper()
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		var body struct{ Data map[string]any }
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body.Data
	}
	check := func(issue map[string]any) {
		t.Helper()
		if issue["priority"] != "P1" || issue["priority_label_warning"] != warning {
			t.Fatalf("diagnostic/priority lost: %v", issue)
		}
	}
	data := request("/v1/issues")
	check(data["issues"].([]any)[0].(map[string]any))
	data = request("/v1/boards/bd-all-issues")
	check(data["issues"].([]any)[0].(map[string]any)["issue"].(map[string]any))
	if f.gets != 0 || f.activityCalls != 0 || f.lists != 2 {
		t.Fatalf("decorating bulk records made extra reads: gets=%d activities=%d lists=%d", f.gets, f.activityCalls, f.lists)
	}
	data = request("/v1/issues/gh-1")
	check(data["issue"].(map[string]any))
	if f.gets != 1 {
		t.Fatalf("detail decoration added GETs: %d", f.gets)
	}
}

func TestSQLiteIssueDTOOmitsGitHubPriorityWarning(t *testing.T) {
	data, err := json.Marshal(IssueToDTO(&models.Issue{ID: "td-1", Priority: models.PriorityP1}))
	if err != nil || strings.Contains(string(data), "priority_label_warning") {
		t.Fatalf("SQLite contract changed: %s %v", data, err)
	}
}

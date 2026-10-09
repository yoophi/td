package serve

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/testutil"
)

func TestGitHubHTTPBulkPageCostMatrix(t *testing.T) {
	for _, route := range []string{"/v1/monitor", "/v1/stats", "/v1/issues", "/v1/labels"} {
		for _, counts := range [][2]int{{0, 0}, {1, 0}, {1, 101}, {45, 0}, {45, 101}, {101, 0}, {101, 101}} {
			t.Run(fmt.Sprintf("%s/issues%d/comments%d", route, counts[0], counts[1]), func(t *testing.T) {
				fixture := testutil.NewGitHubCostFixture(t, counts[0], counts[1])
				t.Setenv("TD_GH_DEBUG", "0")
				ctx, cost := ghstore.WithAPICost(context.Background())
				store := NewGitHubReadStore(fixture.Directory, models.GitHubStoreConfig{Remote: "origin", Repo: "owner/repo"})
				srv := NewGitHubServer(fixture.Directory, "fixture-actual-actor", "owner/repo", ServeConfig{})
				srv.EnableGitHubReads(store)
				srv.EnableGitHubMonitor(store, &githubMonitorFocusFixture{})
				srv.EnableGitHubStats(store)
				response := httptest.NewRecorder()
				request := httptest.NewRequest("GET", route, nil).WithContext(ctx)
				srv.Handler().ServeHTTP(response, request)
				if response.Code != 200 {
					t.Fatalf("HTTP %d: %s", response.Code, response.Body.String())
				}
				comments := 0
				if route == "/v1/monitor" || route == "/v1/stats" {
					comments = fixture.CommentPages
				}
				got := cost.Snapshot()
				if got.Total.HTTPResponses != 1+fixture.IssuePages+comments || got.Categories["preflight"].HTTPResponses != 1 || got.Categories["issue_pages"].HTTPResponses != fixture.IssuePages || got.Categories["comment_pages"].HTTPResponses != comments {
					t.Fatalf("route=%s cost=%+v", route, got)
				}
				for _, category := range []string{"issue_detail", "issue_comments", "comment_detail", "review_validation", "writes"} {
					if got.Categories[category].Invocations != 0 {
						t.Fatalf("N+1/write in aggregate: %+v", got)
					}
				}
			})
		}
	}
}

func TestGitHubHTTPDebugCostUsesRoutePatternAndHidesUserData(t *testing.T) {
	fixture := testutil.NewGitHubCostFixture(t, 1, 101)
	t.Setenv("TD_GH_DEBUG", "1")
	capture, err := os.CreateTemp(t.TempDir(), "cost-log")
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stderr
	os.Stderr = capture
	defer func() { os.Stderr = previous; _ = capture.Close() }()
	srv := NewGitHubServer(fixture.Directory, "actor", "owner/repo", ServeConfig{})
	srv.mux.HandleFunc("GET /v1/cost-test/{id}", func(w http.ResponseWriter, r *http.Request) {
		client, err := ghstore.Open(r.Context(), fixture.Directory, &models.GitHubStoreConfig{Remote: "origin", Repo: "owner/repo"})
		if err == nil {
			_, err = client.ChangeToken(r.Context())
		}
		if err != nil {
			WriteError(w, "store_error", err.Error(), 502)
			return
		}
		WriteSuccess(w, map[string]any{"ok": true}, 200)
	})
	response := httptest.NewRecorder()
	srv.Handler().ServeHTTP(response, httptest.NewRequest("GET", "/v1/cost-test/private-request-id?secret=private-query", nil))
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	data, err := os.ReadFile(capture.Name())
	if err != nil {
		t.Fatal(err)
	}
	log := string(data)
	for _, want := range []string{`route="GET /v1/cost-test/{id}"`, `http_responses=4`, `preflight_http=1`, `issue_pages_http=1`, `comment_pages_http=2`} {
		if !strings.Contains(log, want) {
			t.Fatalf("missing %s: %s", want, log)
		}
	}
	for _, private := range []string{"private-fixture", "private-request-id", "private-query", "Authorization", "Cookie"} {
		if strings.Contains(log, private) {
			t.Fatalf("cost trace leaked %s", private)
		}
	}
}

package serve

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/testutil"
	"github.com/marcus/td/pkg/monitor"
)

func TestGitHubDashboardTUIAndEventTokenShareUncachedPaginatedSweep(t *testing.T) {
	fixture := testutil.NewGitHubCostFixture(t, 101, 101)
	t.Setenv("TD_GH_DEBUG", "0")
	// Real gh subprocesses with paginated responses; network latency allows the
	// independently validated HTTP/TUI/event-token readers to overlap.
	scriptPath := filepath.Join(strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0], "gh")
	script, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatal(err)
	}
	slowed := strings.ReplaceAll(string(script), "*) cat ", "*) sleep 1; cat ")
	slowed = strings.ReplaceAll(slowed, "--include') printf ", "--include') sleep 1; printf ")
	if err := os.WriteFile(scriptPath, []byte(slowed), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := models.GitHubStoreConfig{Remote: "origin", Repo: "owner/repo"}
	contexts := make([]context.Context, 3)
	costs := make([]*ghstore.APICost, 3)
	for i := range contexts {
		contexts[i], costs[i] = ghstore.WithAPICost(context.Background())
	}
	store := NewGitHubReadStore(fixture.Directory, cfg)
	server := NewGitHubServer(fixture.Directory, "actual-web-actor", cfg.Repo, ServeConfig{})
	server.EnableGitHubStats(store)
	source := monitor.NewGitHubDataSource(contexts[1], fixture.Directory, cfg, "actual-tui-actor", "branch", func(context.Context) (*string, error) { return nil, nil })
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		<-start
		client, err := ghstore.Open(contexts[0], fixture.Directory, &cfg)
		if err != nil {
			t.Error(err)
			return
		}
		token, err := client.ChangeToken(contexts[0])
		if err != nil || !strings.HasPrefix(token, "gh-") {
			t.Errorf("event token failed: %s %v", token, err)
		}
	})
	wg.Go(func() {
		<-start
		msg := source.Fetch("", true, monitor.SortByPriority)
		if msg.Error != nil || len(msg.TaskList.PendingOther) != 101 {
			t.Errorf("TUI failed: %v", msg.Error)
		}
	})
	wg.Go(func() {
		<-start
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, httptest.NewRequest("GET", "/v1/stats", nil).WithContext(contexts[2]))
		if response.Code != 200 {
			t.Errorf("dashboard failed: %d %s", response.Code, response.Body.String())
		}
	})
	close(start)
	wg.Wait()
	var total, preflight, issues, comments, auth int
	for _, cost := range costs {
		s := cost.Snapshot()
		total += s.Total.HTTPResponses
		auth += s.AuthInvocations
		preflight += s.Categories["preflight"].HTTPResponses
		issues += s.Categories["issue_pages"].HTTPResponses
		comments += s.Categories["comment_pages"].HTTPResponses
		for _, key := range []string{"issue_detail", "issue_comments", "review_validation", "writes"} {
			if s.Categories[key].Invocations != 0 {
				t.Fatalf("individual or write call in aggregate: %+v", s)
			}
		}
	}
	if total != 5 || preflight != 1 || issues != 2 || comments != 2 || auth != 3 {
		t.Fatalf("shared cost: total=%d preflight=%d issues=%d comments=%d auth=%d", total, preflight, issues, comments, auth)
	}
}

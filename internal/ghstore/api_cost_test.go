package ghstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/testutil"
)

func TestAggregateConsumerCostMatrix(t *testing.T) {
	for _, consumer := range []string{"ChangeToken", "stats", "JSON export", "Markdown export", "list"} {
		for _, counts := range [][2]int{{0, 0}, {1, 0}, {1, 101}, {45, 0}, {45, 101}, {101, 0}, {101, 101}} {
			t.Run(fmt.Sprintf("%s/issues%d/comments%d", consumer, counts[0], counts[1]), func(t *testing.T) {
				fixture := testutil.NewGitHubCostFixture(t, counts[0], counts[1])
				ctx, cost := WithAPICost(context.Background())
				client, err := Open(ctx, fixture.Directory, &models.GitHubStoreConfig{Remote: "origin", Repo: "owner/repo"})
				if err != nil {
					t.Fatal(err)
				}
				history := true
				switch consumer {
				case "ChangeToken":
					_, err = client.ChangeToken(ctx)
				case "stats":
					_, err = client.ExtendedStats(ctx, time.Now())
				case "JSON export":
					_, err = client.ExportIssues(ctx, true, true)
				case "Markdown export":
					_, err = client.ExportIssues(ctx, true, false)
					history = false
				case "list":
					_, err = client.List(ctx, true)
					history = false
				}
				if err != nil {
					t.Fatal(err)
				}
				got := cost.Snapshot()
				comments := 0
				if history {
					comments = fixture.CommentPages
				}
				if got.Total.HTTPResponses != 1+fixture.IssuePages+comments || got.Categories["preflight"].HTTPResponses != 1 || got.Categories["issue_pages"].HTTPResponses != fixture.IssuePages || got.Categories["comment_pages"].HTTPResponses != comments {
					t.Fatalf("cost=%+v expected I=%d C=%d", got, fixture.IssuePages, comments)
				}
				calls := 2
				if comments > 0 {
					calls++
				}
				if got.Total.Invocations != calls || got.AuthInvocations != 1 {
					t.Fatalf("gh invocation/pagination conflated: %+v", got)
				}
				for _, category := range []string{"issue_detail", "issue_comments", "comment_detail", "review_validation", "writes"} {
					if got.Categories[category].Invocations != 0 {
						t.Fatalf("N+1 or write in aggregate: %+v", got)
					}
				}
			})
		}
	}
}

func TestAPICostScopesAreConcurrentIndependentAndSensitiveArgumentsAreDiscarded(t *testing.T) {
	ctx, left := WithAPICost(context.Background())
	other, right := WithAPICost(context.Background())
	args := []string{"api", "--hostname", "github.com", "--method", "GET", "repos/private-owner/private-repo/issues?secret=query", "-H", "Authorization: secret-token"}
	var wg sync.WaitGroup
	for range 100 {
		wg.Go(func() { recordAPICost(ctx, args, 2) })
	}
	wg.Go(func() { recordAPICost(other, args, 1) })
	wg.Wait()
	if left.Snapshot().Total != (APICounter{Invocations: 100, HTTPResponses: 200}) || right.Snapshot().Total != (APICounter{Invocations: 1, HTTPResponses: 1}) {
		t.Fatal("scope counters mixed")
	}
	for _, item := range []struct {
		ctx  context.Context
		args []string
		want string
	}{
		{ctx, []string{"api", "repos/owner/repo"}, "preflight"},
		{ctx, []string{"api", "--method", "POST", "repos/owner/repo/labels"}, "label_setup"},
		{ctx, []string{"api", "--method", "PATCH", "repos/owner/repo/issues/1"}, "writes"},
		{withReadbackCost(ctx), args, "write_readback"},
		{withReviewCost(ctx), args, "review_validation"},
	} {
		if got := apiCostCategory(item.ctx, item.args); got != item.want {
			t.Fatalf("%s != %s", got, item.want)
		}
	}
}

func TestAPICostCacheHitDoesNotInvokeCollection(t *testing.T) {
	c, _ := cachedSnapshotFixture(t)
	if _, err := c.ReadSnapshot(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	ctx, cost := WithAPICost(context.Background())
	if _, err := c.ReadSnapshot(ctx, true); err != nil {
		t.Fatal(err)
	}
	if got := cost.Snapshot(); got.Total.Invocations != 0 || got.CacheHits != 1 || got.CacheMisses != 0 {
		t.Fatalf("cache metrics=%+v", got)
	}
}

func TestFailedPaginationCostNeverPublishesSuccessfulCache(t *testing.T) {
	for _, kind := range []string{"rate limit", "malformed JSON", "malformed headers"} {
		t.Run(kind, func(t *testing.T) {
			fixture := testutil.NewGitHubCostFixture(t, 101, 101)
			ctx, cost := WithAPICost(context.Background())
			client, err := Open(ctx, fixture.Directory, &models.GitHubStoreConfig{Remote: "origin", Repo: "owner/repo"})
			if err != nil {
				t.Fatal(err)
			}
			client.cache = &snapshotCache{root: filepath.Join(t.TempDir(), "td", "gh-issue", "v1"), repo: client.repo, credential: cacheHash("private-fixture-token")}
			response := "[HTTP/2 200 OK\r\n\r\n[]\n,HTTP/2 200 OK\r\n\r\n{\"not\":\"a comment page\"}]"
			if kind == "rate limit" {
				response = "[HTTP/2 200 OK\r\n\r\n[]\n,HTTP/2 429 Too Many Requests\r\nRetry-After: 3600\r\n\r\n{\"message\":\"limited\"}]"
				path, err := exec.LookPath("gh")
				if err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				script := strings.Replace(string(data), `cat "$TD_COST_FIXTURE_COMMENTS" ;;`, `cat "$TD_COST_FIXTURE_COMMENTS"; printf 'original-partial-request-id\n' >&2; exit 1 ;;`, 1)
				if err := os.WriteFile(path, []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "malformed headers" {
				response = "[HTTP/2 200 OK\r\n\r\n[]\n,HTTP/2 200 OK\r\nmalformed header\r\n\r\n[]]"
			}
			if err := os.WriteFile(os.Getenv("TD_COST_FIXTURE_COMMENTS"), []byte(response), 0600); err != nil {
				t.Fatal(err)
			}
			snapshot, err := client.ReadSnapshot(ctx, true)
			if err == nil || snapshot != nil {
				t.Fatal("partial pagination succeeded")
			}
			if _, fileErr := os.Stat(client.cache.path(true)); !errors.Is(fileErr, os.ErrNotExist) {
				t.Fatal("failure published a cache", fileErr)
			}
			got := cost.Snapshot()
			if got.Total.HTTPResponses != 5 || got.Total.Invocations != 3 || got.Categories["comment_pages"].HTTPResponses != 2 || got.CacheMisses != 1 {
				t.Fatalf("partial response accounting lost: %+v", got)
			}
			if kind == "rate limit" {
				var limit *RateLimitError
				if !errors.As(err, &limit) || limit.WaitSource != "retry-after" || !strings.Contains(err.Error(), "original-partial-request-id") {
					t.Fatalf("original/reset lost: %v", err)
				}
			}
		})
	}
}

package serve

import (
	"context"
	"errors"
	"fmt"
	"github.com/marcus/td/internal/ghstore"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGitHubRateLimitHTTPHelpersPreserveUncertainWriteAndPermissionErrors(t *testing.T) {
	limit := fmt.Errorf("write may have reached GitHub: %w", &ghstore.RateLimitError{Cause: errors.New("HTTP 429")})
	for _, write := range []func(http.ResponseWriter, error){readError, githubWriteError, sessionStoreError} {
		w := httptest.NewRecorder()
		write(w, limit)
		if w.Code != 429 || w.Header().Get("Retry-After") != "60" || !strings.Contains(w.Body.String(), `"code":"rate_limited"`) || !strings.Contains(w.Body.String(), "write may have reached GitHub") {
			t.Fatalf("%d %s %s", w.Code, w.Header(), w.Body.String())
		}
		w = httptest.NewRecorder()
		write(w, errors.New("HTTP 403: Resource not accessible"))
		if w.Code != 502 || w.Header().Get("Retry-After") != "" {
			t.Fatalf("permission error classified as rate limit: %d", w.Code)
		}
	}
}
func TestGitHubRateLimitStatsAndSSEInitialHTTP(t *testing.T) {
	limit := &ghstore.RateLimitError{Cause: errors.New("HTTP 403: API rate limit exceeded")}
	store := &GitHubReadStore{open: func(context.Context) (githubReadClient, error) { return nil, limit }}
	srv := NewGitHubServer(t.TempDir(), "web", "owner/repo", ServeConfig{})
	srv.EnableGitHubReads(store)
	srv.EnableGitHubStats(store)
	srv.EnableGitHubEvents(store)
	srv.githubEvents.observe(context.Background())
	for _, path := range []string{"/v1/issues", "/v1/stats", "/v1/labels", "/v1/events"} {
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 429 || w.Header().Get("Retry-After") != "60" || !strings.Contains(w.Body.String(), "rate_limited") {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
	srv.StopBackground()
}
func TestGitHubRateLimitSSEBackoffAndLongConfiguredInterval(t *testing.T) {
	failure := error(nil)
	h := newGitHubEventHub(func(context.Context) (string, error) { return "gh-one", failure }, 30*time.Second)
	h.observe(context.Background())
	ch, _, err := h.register()
	if err != nil {
		t.Fatal(err)
	}
	failure = &ghstore.RateLimitError{Cause: errors.New("HTTP 429")}
	h.observe(context.Background())
	event := <-ch
	if event.Event != "store_error" || event.ID != "gh-one" || !strings.Contains(event.Data, `"code":"rate_limited"`) || !strings.Contains(event.Data, `"retry_after":60`) {
		t.Fatalf("%+v", event)
	}
	if delay := h.failureDelay(30*time.Second, false); delay != time.Minute {
		t.Fatalf("initial rate delay=%v", delay)
	}
	if delay := h.failureDelay(5*time.Minute, true); delay != 5*time.Minute {
		t.Fatalf("backoff cap=%v", delay)
	}
	h.interval = 10 * time.Minute
	if delay := h.failureDelay(10*time.Minute, true); delay != 10*time.Minute {
		t.Fatalf("long interval shortened=%v", delay)
	}
	h.stop()
}

func TestGitHubObservedRateDeadlineHTTPAndPolling(t *testing.T) {
	limit := &ghstore.RateLimitError{Cause: errors.New("HTTP 429"), RetryAt: time.Now().Add(time.Hour), WaitSource: "retry-after"}
	w := httptest.NewRecorder()
	githubWriteError(w, fmt.Errorf("write may have reached GitHub: %w", limit))
	if w.Code != 429 || w.Header().Get("Retry-After") != "3600" || !strings.Contains(w.Body.String(), "retry-after") {
		t.Fatalf("%d %s %s", w.Code, w.Header(), w.Body.String())
	}
	h := newGitHubEventHub(func(context.Context) (string, error) { return "", limit }, 30*time.Second)
	h.observe(context.Background())
	if wait := h.failureDelay(5*time.Minute, true); wait < 3599*time.Second {
		t.Fatalf("reset deadline capped to short backoff: %v", wait)
	}
	h.stop()
}

func TestGitHubRateLimitPreservesOriginalDiagnostic(t *testing.T) {
	original := "gh: API rate limit exceeded (HTTP 403); request ID D352:CDDF:3F8B1:54D38:6AC9023F; timestamp 2026-10-09 15:03:27 UTC"
	limit := &ghstore.RateLimitError{Cause: errors.New(original), RetryAt: time.Now().Add(time.Hour), WaitSource: "x-ratelimit-reset"}
	w := httptest.NewRecorder()
	if !writeGitHubRateLimit(w, limit) || w.Code != 429 || !strings.Contains(w.Body.String(), original) || !strings.Contains(w.Body.String(), "GitHub rate limit reached") || !strings.Contains(w.Body.String(), "x-ratelimit-reset") {
		t.Fatalf("original error or exception guidance missing: %s", w.Body.String())
	}
}

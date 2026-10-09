package serve

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGitHubLifecycleCancelsActiveBackendAndRejectsNewRequests(t *testing.T) {
	for _, mode := range []string{"stop", "parent", "shutdown", "client"} {
		t.Run(mode, func(t *testing.T) {
			srv := NewGitHubServer(t.TempDir(), "web", "owner/repo", ServeConfig{})
			parent, cancelParent := context.WithCancel(context.Background())
			defer cancelParent()
			srv.StartBackground(parent)
			defer srv.StopBackground()
			entered := make(chan struct{})
			result := make(chan error, 1)
			done := make(chan struct{})
			srv.EnableGitHubReads(&GitHubReadStore{open: func(ctx context.Context) (githubReadClient, error) {
				close(entered)
				<-ctx.Done()
				result <- ctx.Err()
				return nil, ctx.Err()
			}})
			requestCtx, cancelRequest := context.WithCancel(context.Background())
			defer cancelRequest()
			go func() {
				defer close(done)
				srv.Handler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/v1/issues", nil).WithContext(requestCtx))
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("backend not started")
			}
			switch mode {
			case "stop":
				srv.StopBackground()
			case "parent":
				cancelParent()
			case "shutdown":
				if err := srv.Shutdown(context.Background()); err != nil {
					t.Fatal(err)
				}
			case "client":
				cancelRequest()
			}
			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("backend did not receive cancellation")
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("handler did not finish")
			}
			w := httptest.NewRecorder()
			srv.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/health", nil))
			want := 503
			if mode == "client" {
				want = 200
			}
			if w.Code != want {
				t.Fatalf("new request after %s: %d %s", mode, w.Code, w.Body.String())
			}
		})
	}
}
func TestGitHubLifecycleAlreadyCanceledParentAndRepeatedStop(t *testing.T) {
	srv := NewGitHubServer(t.TempDir(), "web", "owner/repo", ServeConfig{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	srv.StartBackground(ctx)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/health", nil))
	if w.Code != 503 {
		t.Fatalf("canceled parent accepted request: %d", w.Code)
	}
	srv.StopBackground()
	srv.StopBackground()
	srv.StartBackground(context.Background())
	w = httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/health", nil))
	if w.Code != 503 {
		t.Fatal("stopped lifecycle restarted")
	}
}

package serve

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGitHubEventHubChangeFailureRecoveryAndSlowClient(t *testing.T) {
	token := "gh-one"
	failure := error(nil)
	h := newGitHubEventHub(func(context.Context) (string, error) { return token, failure }, time.Second)
	if _, _, err := h.register(); err == nil {
		t.Fatal("pending snapshot accepted")
	}
	if !h.observe(context.Background()) {
		t.Fatal("initial read failed")
	}
	ch, current, err := h.register()
	if err != nil || current != token {
		t.Fatal(err)
	}
	if !h.observe(context.Background()) {
		t.Fatal("unchanged failed")
	}
	select {
	case <-ch:
		t.Fatal("unchanged snapshot broadcast")
	default:
	}
	token = "gh-two"
	h.observe(context.Background())
	event := <-ch
	if event.Event != "refresh" || event.ID != token {
		t.Fatalf("%+v", event)
	}
	failure = errors.New("permission denied")
	h.observe(context.Background())
	event = <-ch
	if event.Event != "store_error" || event.ID != "gh-two" || !strings.Contains(event.Data, "permission denied") {
		t.Fatalf("%+v", event)
	}
	if _, _, err := h.register(); err == nil {
		t.Fatal("failed observation accepted fresh subscriber")
	}
	failure = nil
	h.observe(context.Background())
	if event = <-ch; event.Event != "refresh" || event.ID != token {
		t.Fatal("recovery did not refresh")
	}
	// Dropping a slow connection forces resubscription/current-token comparison
	// instead of silently losing its last refresh forever.
	for i := 0; i < 20; i++ {
		token += "x"
		h.observe(context.Background())
	}
	for range ch {
	}
	h.stop()
	h.stop()
	if _, _, err := h.register(); err == nil {
		t.Fatal("stopped stream accepted")
	}
}
func TestGitHubEventHubShutdownCancelsPoll(t *testing.T) {
	entered := make(chan struct{})
	h := newGitHubEventHub(func(ctx context.Context) (string, error) { close(entered); <-ctx.Done(); return "", ctx.Err() }, time.Second)
	h.start(context.Background())
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("poll not started")
	}
	stopped := make(chan struct{})
	go func() { h.stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("stop did not cancel poll")
	}
	h.stop()
	h.start(context.Background())
}

type cancelOnFlush struct {
	*httptest.ResponseRecorder
	cancel context.CancelFunc
}

func (w cancelOnFlush) Flush() { w.ResponseRecorder.Flush(); w.cancel() }
func TestGitHubEventsHTTPInitialReconnectFailuresAndQuery(t *testing.T) {
	for _, last := range []string{"", "gh-current", "gh-stale"} {
		t.Run(last, func(t *testing.T) {
			srv := NewGitHubServer(t.TempDir(), "web", "owner/repo", ServeConfig{})
			srv.EnableGitHubEvents(&GitHubReadStore{})
			if srv.githubEvents.interval != 30*time.Second {
				t.Fatal("unbounded poll frequency")
			}
			srv.githubEvents.read = func(context.Context) (string, error) { return "gh-current", nil }
			srv.githubEvents.observe(context.Background())
			health := httptest.NewRecorder()
			srv.Handler().ServeHTTP(health, httptest.NewRequest("GET", "/health", nil))
			if health.Code != 200 || !strings.Contains(health.Body.String(), `"change_token":"gh-current"`) {
				t.Fatalf("health token missing: %s", health.Body.String())
			}
			project := httptest.NewRecorder()
			srv.Handler().ServeHTTP(project, httptest.NewRequest("GET", "/v1/project", nil))
			if !strings.Contains(project.Body.String(), `"events"`) || !strings.Contains(project.Body.String(), `GET /v1/events`) {
				t.Fatal("events support matrix missing")
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req := httptest.NewRequest("GET", "/v1/events", nil).WithContext(ctx)
			req.Header.Set("Last-Event-ID", last)
			recorder := httptest.NewRecorder()
			srv.Handler().ServeHTTP(cancelOnFlush{recorder, cancel}, req)
			event := "ping"
			if last == "gh-stale" {
				event = "refresh"
			}
			if recorder.Code != 200 || recorder.Header().Get("Content-Type") != "text/event-stream" || !strings.Contains(recorder.Body.String(), "id: gh-current\nevent: "+event) {
				t.Fatalf("%d %s", recorder.Code, recorder.Body.String())
			}
			srv.githubEvents.mu.Lock()
			count := len(srv.githubEvents.clients)
			srv.githubEvents.mu.Unlock()
			if count != 0 {
				t.Fatal("disconnected subscriber retained")
			}
			srv.StopBackground()
		})
	}
	srv := NewGitHubServer(t.TempDir(), "web", "owner/repo", ServeConfig{})
	srv.EnableGitHubEvents(&GitHubReadStore{})
	for _, path := range []string{"/v1/events", "/v1/events?unexpected=1"} {
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		want := 503
		if strings.Contains(path, "?") {
			want = 400
		}
		if w.Code != want || strings.Contains(w.Header().Get("Content-Type"), "event-stream") {
			t.Fatalf("pending/query incorrectly streamed: %d %s", w.Code, w.Body.String())
		}
	}
	srv.githubEvents.read = func(context.Context) (string, error) { return "", errors.New("gh unavailable") }
	srv.githubEvents.observe(context.Background())
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/v1/events", nil))
	if w.Code != 503 || !strings.Contains(w.Body.String(), "gh unavailable") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	srv.StopBackground()
}

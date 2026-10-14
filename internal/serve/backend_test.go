package serve

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marcus/td/internal/backendstore"
)

const backendTestProject = "prj_0199dc38-1200-7000-8000-000000000001"
const backendTestSession = "ses_0199dc38-1200-7000-8000-000000000002"

func testBackendServer(t *testing.T, upstream http.Handler, config ServeConfig) (*Server, *httptest.Server, *httptest.Server) {
	t.Helper()
	backend := httptest.NewServer(upstream)
	client, err := backendstore.NewClient(backend.URL, "backend-secret")
	if err != nil {
		t.Fatal(err)
	}
	client.HTTP.Timeout = 100 * time.Millisecond // SSE must not inherit the CLI timeout.
	server := NewBackendServer(t.TempDir(), &backendstore.Connection{Client: client, ProjectID: backendTestProject, SessionID: backendTestSession}, backendstore.Project{ID: backendTestProject, Name: "remote", TitleMinLength: 1, TitleMaxLength: 200}, config)
	local := httptest.NewServer(server.Handler())
	t.Cleanup(func() { server.StopBackground(); local.Close(); backend.Close() })
	return server, local, backend
}

func TestBackendServeProjectAndProxyCredentials(t *testing.T) {
	var calls atomic.Int32
	server, local, _ := testBackendServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer backend-secret" || r.Header.Get("X-TD-Session") != backendTestSession || r.Header.Get("X-Injected") != "" {
			t.Errorf("backend headers: %v", r.Header)
		}
		switch r.URL.Path {
		case "/v1/projects/" + backendTestProject + "/changes":
			_, _ = io.WriteString(w, `{"ok":true,"data":{"change_token":"c-1"}}`)
		case "/v1/projects/" + backendTestProject + "/issues":
			if r.URL.RawQuery != "limit=3" {
				t.Errorf("query = %q", r.URL.RawQuery)
			}
			_, _ = io.WriteString(w, `{"ok":true,"data":{"issues":[]}}`)
		default:
			t.Errorf("unexpected backend path %s", r.URL.Path)
		}
	}), ServeConfig{Token: "local-secret"})
	_ = server
	request := func(path, token string) *http.Response {
		t.Helper()
		r, _ := http.NewRequest(http.MethodGet, local.URL+path, nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		r.Header.Set("X-TD-Session", "browser-spoof")
		r.Header.Set("X-Injected", "browser-spoof")
		response, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	response := request("/v1/project", "")
	if response.StatusCode != 401 {
		t.Fatalf("unauthenticated project status %d", response.StatusCode)
	}
	response.Body.Close()
	response = request("/health", "")
	if response.StatusCode != 200 {
		t.Fatalf("health status %d", response.StatusCode)
	}
	response.Body.Close()
	response = request("/v1/project", "local-secret")
	var project struct {
		Data map[string]any `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&project); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if project.Data["store"] != "td-backend" || project.Data["session_id"] != backendTestSession || project.Data["name"] != "remote" {
		t.Fatalf("project = %#v", project.Data)
	}
	response = request("/v1/issues?limit=3", "local-secret")
	data, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || !strings.Contains(string(data), `"issues":[]`) || strings.Contains(string(data), "backend-secret") {
		t.Fatalf("issues status=%d body=%s", response.StatusCode, data)
	}
	if calls.Load() != 2 {
		t.Fatalf("backend calls = %d", calls.Load())
	}
}

func TestBackendServeMutationPreservesOrObservesRevision(t *testing.T) {
	var observed atomic.Int32
	var mutated atomic.Int32
	_, local, _ := testBackendServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer backend-secret" || r.Header.Get("X-TD-Session") != backendTestSession {
			t.Errorf("backend credentials missing")
		}
		if r.Method == http.MethodGet {
			observed.Add(1)
			w.Header().Set("ETag", `"r-7"`)
			_, _ = io.WriteString(w, `{"ok":true,"data":{"issue":{}}}`)
			return
		}
		mutated.Add(1)
		if r.Header.Get("If-Match") != `"r-7"` || r.Header.Get("Idempotency-Key") == "" {
			t.Errorf("mutation headers: %v", r.Header)
		}
		w.Header().Set("ETag", `"r-8"`)
		_, _ = io.WriteString(w, `{"ok":true,"data":{"issue":{"revision":"r-8"}}}`)
	}), ServeConfig{Token: "local-secret"})
	for _, match := range []string{"", `"r-7"`} {
		r, _ := http.NewRequest(http.MethodPatch, local.URL+"/v1/issues/td-1234567890abcdef", strings.NewReader(`{"title":"edited"}`))
		r.Header.Set("Authorization", "Bearer local-secret")
		r.Header.Set("Content-Type", "application/json")
		if match != "" {
			r.Header.Set("If-Match", match)
		}
		response, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 200 || response.Header.Get("ETag") != `"r-8"` {
			t.Fatalf("mutation status=%d, etag=%q", response.StatusCode, response.Header.Get("ETag"))
		}
	}
	if observed.Load() != 1 {
		t.Fatalf("revision observations=%d, want one", observed.Load())
	}
	oversized, _ := http.NewRequest(http.MethodPatch, local.URL+"/v1/issues/td-1234567890abcdef", strings.NewReader(strings.Repeat("x", (1<<20)+1)))
	oversized.Header.Set("Authorization", "Bearer local-secret")
	oversized.Header.Set("If-Match", `"r-7"`)
	response, err := http.DefaultClient.Do(oversized)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusRequestEntityTooLarge || mutated.Load() != 2 {
		t.Fatalf("oversized request status=%d, backend mutations=%d", response.StatusCode, mutated.Load())
	}
}

func TestBackendServeEventsFlushAndShutdown(t *testing.T) {
	upstreamDone := make(chan struct{})
	server, local, _ := testBackendServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(upstreamDone)
		if r.URL.Path != "/v1/projects/"+backendTestProject+"/events" || r.Header.Get("Last-Event-ID") != "c-old" || r.Header.Get("Authorization") != "Bearer backend-secret" {
			t.Errorf("event request path=%s headers=%v", r.URL.Path, r.Header)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "id: c-new\nevent: refresh\ndata: {\"change_token\":\"c-new\"}\n\n")
		w.(http.Flusher).Flush()
		time.Sleep(150 * time.Millisecond)
		_, _ = io.WriteString(w, "id: c-new\nevent: ping\ndata: {\"change_token\":\"c-new\"}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}), ServeConfig{Token: "local-secret"})
	request, _ := http.NewRequest(http.MethodGet, local.URL+"/v1/events", nil)
	request.Header.Set("Authorization", "Bearer local-secret")
	request.Header.Set("Last-Event-ID", "c-old")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || response.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("event status=%d headers=%v", response.StatusCode, response.Header)
	}
	reader := bufio.NewReader(response.Body)
	line, err := reader.ReadString('\n')
	if err != nil || line != "id: c-new\n" {
		t.Fatalf("unflushed refresh line %q, %v", line, err)
	}
	for {
		line, err = reader.ReadString('\n')
		if err != nil {
			t.Fatalf("SSE disconnected at typed client timeout: %v", err)
		}
		if line == "event: ping\n" {
			break
		}
	}
	if err := server.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	select {
	case <-upstreamDone:
	case <-ctx.Done():
		t.Fatal("backend stream did not cancel on serve shutdown")
	}
}

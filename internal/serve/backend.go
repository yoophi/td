package serve

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/marcus/td/internal/backendstore"
)

// NewBackendServer exposes the td serve contract over a project-scoped remote
// backend. Only routes in the GUI contract are registered; the local middleware
// continues to own browser authentication and CORS.
func NewBackendServer(baseDir string, conn *backendstore.Connection, project backendstore.Project, config ServeConfig) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{baseDir: baseDir, sessionID: conn.SessionID, config: config, mux: http.NewServeMux(), backendContext: ctx, backendCancel: cancel}
	s.mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		token, err := conn.ChangeToken(r.Context())
		if err != nil {
			WriteError(w, ErrInternal, "backend health unavailable", http.StatusServiceUnavailable)
			return
		}
		WriteSuccess(w, map[string]any{"status": "ok", "session_id": conn.SessionID, "change_token": token, "store": "td-backend"}, http.StatusOK)
	})
	s.mux.HandleFunc("GET /v1/project", func(w http.ResponseWriter, r *http.Request) {
		WriteSuccess(w, map[string]any{
			"name": project.Name, "path": baseDir, "session_id": conn.SessionID,
			"title_min_length": project.TitleMinLength, "title_max_length": project.TitleMaxLength,
			"store": "td-backend", "project_id": conn.ProjectID,
			"capabilities": []string{"issue_revisions", "board_move", "sessions", "focus", "monitor", "events", "notes", "work_sessions"},
		}, http.StatusOK)
	})
	proxy := func(w http.ResponseWriter, r *http.Request) { s.proxyBackend(conn, w, r) }
	add := func(methods, path string) {
		for _, method := range strings.Fields(methods) {
			s.mux.HandleFunc(method+" /v1/"+path, proxy)
		}
	}
	add("GET POST", "sessions")
	add("POST", "sessions/{session_id}/heartbeat")
	add("GET POST", "issues")
	add("GET PATCH DELETE", "issues/{issue_id}")
	for _, action := range []string{"restore", "start", "unstart", "review", "approve", "reject", "block", "unblock", "close", "reopen", "reviews", "status", "comments", "logs", "handoffs", "dependencies"} {
		add("POST", "issues/{issue_id}/"+action)
	}
	add("GET POST", "issues/{issue_id}/activity")
	add("DELETE", "issues/{issue_id}/comments/{comment_id}")
	add("DELETE", "issues/{issue_id}/dependencies/{dep_id}")
	add("PUT", "issues/{issue_id}/files")
	add("GET POST", "boards")
	add("GET PATCH DELETE", "boards/{board_id}")
	add("POST", "boards/{board_id}/issues")
	add("POST", "boards/{board_id}/move")
	add("DELETE", "boards/{board_id}/issues/{issue_id}")
	add("POST", "boards/{board_id}/visit")
	add("GET", "monitor")
	add("GET PUT", "focus")
	add("GET", "stats")
	add("GET", "labels")
	add("GET", "events")
	add("GET POST", "notes")
	add("GET PATCH DELETE", "notes/{note_id}")
	add("POST", "notes/{note_id}/restore")
	add("GET POST", "work-sessions")
	add("GET", "work-sessions/{work_session_id}")
	add("GET", "work-sessions/{work_session_id}/activity")
	add("GET POST", "work-sessions/{work_session_id}/logs")
	add("PUT", "work-sessions/{work_session_id}/issues")
	add("POST", "work-sessions/{work_session_id}/end")
	return s
}

func (s *Server) proxyBackend(conn *backendstore.Connection, w http.ResponseWriter, r *http.Request) {
	if r.URL.EscapedPath() != r.URL.Path || !strings.HasPrefix(r.URL.Path, "/v1/") {
		WriteError(w, ErrValidation, "invalid API path", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	stop := context.AfterFunc(s.backendContext, cancel)
	defer func() { stop(); cancel() }()
	if r.URL.Path != "/v1/events" {
		var deadlineCancel context.CancelFunc
		ctx, deadlineCancel = context.WithTimeout(ctx, 30*time.Second)
		defer deadlineCancel()
	}
	path := "/v1/projects/" + conn.ProjectID + strings.TrimPrefix(r.URL.Path, "/v1")
	url := conn.Client.URL + path
	if r.URL.RawQuery != "" {
		url += "?" + r.URL.RawQuery
	}
	match := r.Header.Get("If-Match")
	if len(r.Header.Values("If-Match")) > 1 {
		WriteError(w, ErrValidation, "one If-Match header required", http.StatusBadRequest)
		return
	}
	if match == "" && backendNeedsRevision(r) {
		var ok bool
		match, ok = s.observeBackendRevision(ctx, conn, w, r)
		if !ok {
			return
		}
	}
	var body io.Reader
	if r.Body != nil {
		data, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if e != nil {
			WriteError(w, ErrValidation, "request body exceeds limit or could not be read", http.StatusRequestEntityTooLarge)
			return
		}
		body = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, r.Method, url, body)
	if err != nil {
		WriteError(w, ErrValidation, "invalid API request", http.StatusBadRequest)
		return
	}
	request.Header.Set("Authorization", "Bearer "+conn.Client.Token)
	request.Header.Set("X-TD-Session", conn.SessionID)
	for _, name := range []string{"Accept", "Content-Type", "Last-Event-ID"} {
		if value := r.Header.Get(name); value != "" {
			request.Header.Set(name, value)
		}
	}
	if match != "" {
		request.Header.Set("If-Match", match)
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		key := r.Header.Get("Idempotency-Key")
		if key == "" {
			generated, e := uuid.NewRandom()
			if e != nil {
				WriteError(w, ErrInternal, "could not create operation key", http.StatusInternalServerError)
				return
			}
			key = generated.String()
		}
		request.Header.Set("Idempotency-Key", key)
	}
	// The typed CLI client has a 15-second deadline. A browser SSE connection
	// must outlive that deadline, so proxy requests use the same transport and
	// redirect policy with lifetime controlled by their request context.
	proxyClient := &http.Client{Transport: conn.Client.HTTP.Transport, CheckRedirect: conn.Client.HTTP.CheckRedirect}
	response, err := proxyClient.Do(request)
	if err != nil {
		if ctx.Err() == nil {
			WriteError(w, ErrInternal, "backend request unavailable; write outcome may be uncertain", http.StatusServiceUnavailable)
		}
		return
	}
	defer response.Body.Close()
	copyBackendResponse(w, response, r.URL.Path == "/v1/events")
}

func backendNeedsRevision(r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead || r.URL.Path == "/v1/boards" || r.URL.Path == "/v1/issues" || r.URL.Path == "/v1/notes" || r.URL.Path == "/v1/focus" || strings.HasPrefix(r.URL.Path, "/v1/work-sessions") || strings.HasPrefix(r.URL.Path, "/v1/sessions") {
		return false
	}
	if strings.HasSuffix(r.URL.Path, "/visit") {
		return false
	}
	return strings.HasPrefix(r.URL.Path, "/v1/issues/") || strings.HasPrefix(r.URL.Path, "/v1/boards/") || strings.HasPrefix(r.URL.Path, "/v1/notes/")
}

// A missing browser revision can only be observed at mutation time. This
// protects against a concurrent write after the observation, but cannot detect
// that the browser form was already stale before the request reached td serve.
func (s *Server) observeBackendRevision(ctx context.Context, conn *backendstore.Connection, w http.ResponseWriter, r *http.Request) (string, bool) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/"), "/")
	if len(parts) < 2 || parts[1] == "" {
		WriteError(w, ErrValidation, "invalid aggregate path", http.StatusBadRequest)
		return "", false
	}
	url := fmt.Sprintf("%s/v1/projects/%s/%s/%s", conn.Client.URL, conn.ProjectID, parts[0], parts[1])
	if parts[0] == "issues" && len(parts) > 2 && parts[2] == "restore" {
		url += "?with_deleted=true"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		WriteError(w, ErrValidation, "invalid aggregate path", http.StatusBadRequest)
		return "", false
	}
	request.Header.Set("Authorization", "Bearer "+conn.Client.Token)
	request.Header.Set("X-TD-Session", conn.SessionID)
	response, err := conn.Client.HTTP.Do(request)
	if err != nil {
		WriteError(w, ErrInternal, "backend revision unavailable", http.StatusServiceUnavailable)
		return "", false
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		copyBackendResponse(w, response, false)
		return "", false
	}
	if response.Header.Get("ETag") == "" {
		WriteError(w, ErrInternal, "backend revision missing", http.StatusBadGateway)
		return "", false
	}
	return response.Header.Get("ETag"), true
}

func copyBackendResponse(w http.ResponseWriter, response *http.Response, stream bool) {
	if stream && response.StatusCode == http.StatusOK {
		if _, ok := w.(http.Flusher); !ok {
			WriteError(w, ErrInternal, "event stream cannot be flushed", http.StatusServiceUnavailable)
			return
		}
	}
	for _, name := range []string{"Content-Type", "ETag", "Cache-Control", "X-Accel-Buffering"} {
		if value := response.Header.Get(name); value != "" {
			w.Header().Set(name, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	if !stream || response.StatusCode != http.StatusOK {
		_, _ = io.Copy(w, response.Body)
		return
	}
	flusher := w.(http.Flusher)
	flusher.Flush()
	buffer := make([]byte, 4096)
	for {
		n, err := response.Body.Read(buffer)
		if n > 0 {
			if _, writeErr := w.Write(buffer[:n]); writeErr != nil {
				return
			}
			flusher.Flush()
		}
		if err != nil {
			return
		}
	}
}

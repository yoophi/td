package serve

import (
	"net/http"
	"path/filepath"
)

// NewGitHubServer shares the listener and middleware with SQLite, without
// constructing a SQLite handler context. Routes are enabled as their GitHub
// adapters become available; unsupported operations must never reach a DB.
func NewGitHubServer(baseDir, sessionID, repo string, config ServeConfig, sessions ...SessionStore) *Server {
	s := &Server{baseDir: baseDir, sessionID: sessionID, config: config, mux: http.NewServeMux()}
	supported := []string{"GET /health", "GET /v1/project"}
	if len(sessions) > 0 && sessions[0] != nil {
		s.registerSessionStore(sessions[0])
		supported = append(supported, "GET /v1/sessions", "PUT /v1/focus")
	}
	s.mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		WriteSuccess(w, map[string]any{"status": "ok", "session_id": sessionID, "change_token": "", "store": "gh-issue"}, http.StatusOK)
	})
	s.mux.HandleFunc("GET /v1/project", func(w http.ResponseWriter, r *http.Request) {
		min, max := titleLengthLimitsFor(HandlerContext{BaseDir: baseDir})
		WriteSuccess(w, map[string]any{"name": filepath.Base(baseDir), "path": baseDir, "session_id": sessionID, "title_min_length": min, "title_max_length": max, "store": "gh-issue", "repository": repo, "capabilities": []string{}, "supported_endpoints": supported}, http.StatusOK)
	})
	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		WriteError(w, "unsupported_operation", "This HTTP operation is not yet supported by the gh-issue store; no GitHub or SQLite write was attempted.", http.StatusNotImplemented)
	})
	return s
}

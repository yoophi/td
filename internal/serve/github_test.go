package serve

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestGitHubServerBootstrapRoutesWithoutSQLite(t *testing.T) {
	dir := t.TempDir()
	srv := NewGitHubServer(dir, "ses_web_fixture", "owner/repo", ServeConfig{Token: "fixture-token", CORSOrigin: "https://fixture.example"})
	for _, tc := range []struct {
		method, path, token string
		status              int
	}{
		{"GET", "/health", "", 200},
		{"GET", "/v1/project", "", 401},
		{"GET", "/v1/project", "fixture-token", 200},
		{"POST", "/v1/issues", "fixture-token", 501},
		{"GET", "/v1/events", "fixture-token", 501},
		{"GET", "/v1/issues", "", 401},
	} {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		r.Header.Set("Origin", "https://fixture.example")
		if tc.token != "" {
			r.Header.Set("Authorization", "Bearer "+tc.token)
		}
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, w.Code, w.Body.String())
		}
		if w.Header().Get("Access-Control-Allow-Origin") != "https://fixture.example" {
			t.Fatal("CORS lost")
		}
		var response Envelope
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if tc.status == 501 && (response.Error == nil || response.Error.Code != "unsupported_operation") {
			t.Fatalf("%+v", response)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatalf("SQLite unexpectedly exists: %v", err)
	}
	// No SSE hub or SQLite autosync is started by the GitHub bootstrap.
	if srv.db != nil || srv.sseHub != nil {
		t.Fatal("SQLite background runtime enabled")
	}
}

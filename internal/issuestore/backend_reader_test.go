package issuestore

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/models"
)

func TestBackendReaderUsesRegisteredCallerAndNeverLocalDB(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TD_BACKEND_CONTEXT_ROOT", t.TempDir())
	t.Setenv("TD_BACKEND_TOKEN", "reader-fixture-token")
	t.Setenv("TD_SESSION_ID", "reader-context")
	const project = "prj_0199dc38-1200-7000-8000-000000000001"
	const session = "ses_0199dc38-1200-7000-8000-000000000002"
	const issueID = "td-0000000000000001"
	callerRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer reader-fixture-token" {
			t.Error("missing credential")
		}
		var data any
		issue := map[string]any{"id": issueID, "revision": "r-3", "title": "Backend reader issue", "status": "open", "type": "task", "priority": "P2", "parent_id": nil}
		if r.Method == "POST" && r.URL.Path == "/v1/projects/"+project+"/sessions" {
			data = map[string]any{"session": map[string]string{"id": session}}
		} else {
			callerRequests++
			if r.Header.Get("X-TD-Session") != session {
				t.Error("missing owned Session")
			}
			switch r.URL.Path {
			case "/v1/projects/" + project:
				data = map[string]any{"project": map[string]string{"id": project}}
			case "/v1/projects/" + project + "/issues":
				data = map[string]any{"issues": []any{issue}, "total": 1, "limit": 1000, "offset": 0, "has_more": false, "change_token": "opaque"}
			case "/v1/projects/" + project + "/issues/" + issueID:
				data = map[string]any{"issue": issue}
			default:
				w.WriteHeader(404)
				return
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": data})
	}))
	defer server.Close()
	if e := config.SetBackend(dir, models.BackendStoreConfig{URL: server.URL, ProjectID: project}, config.StoreSQLite); e != nil {
		t.Fatal(e)
	}
	reader, e := OpenReader(context.Background(), dir)
	if e != nil {
		t.Fatal(e)
	}
	defer reader.Close()
	records, e := reader.List(context.Background(), true)
	if e != nil || len(records) != 1 || records[0].Revision != "r-3" {
		t.Fatal(records, e)
	}
	record, e := reader.Get(context.Background(), issueID)
	if e != nil || record.ID != issueID || record.Revision != "r-3" {
		t.Fatal(record, e)
	}
	if callerRequests != 3 {
		t.Fatal("wrong caller requests", callerRequests)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = reader.List(ctx, true); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(e) {
		t.Fatal("SQLite fallback", e)
	}
	t.Setenv("TD_BACKEND_TOKEN", "")
	if _, e = OpenReader(context.Background(), dir); e == nil {
		t.Fatal("missing credential accepted")
	}
}

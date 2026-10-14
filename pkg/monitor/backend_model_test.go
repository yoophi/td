package monitor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/models"
)

func TestBackendMonitorFactoryUsesRemoteSessionWithoutSQLite(t *testing.T) {
	const project = "prj_0199f3bb-9ff2-7777-8888-123456789abc"
	const session = "ses_0199f3bb-9ff2-7777-8888-123456789abc"
	dir := t.TempDir()
	root := t.TempDir()
	t.Setenv("TD_BACKEND_CONTEXT_ROOT", root)
	t.Setenv("TD_BACKEND_TOKEN", "monitor-token")
	t.Setenv("TD_CONTEXT_ID", "backend-monitor-test")
	git := exec.Command("git", "init", dir)
	if output, err := git.CombinedOutput(); err != nil {
		t.Fatal(string(output), err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var data any
		switch r.URL.Path {
		case "/healthz":
			data = map[string]any{"status": "ok", "api_version": "v1"}
		case "/v1/capabilities":
			data = map[string]any{"api_version": "v1", "capabilities": []string{"projects", "sessions", "idempotent_init"}}
		case "/v1/projects/" + project + "/sessions":
			data = map[string]any{"session": map[string]string{"id": session}}
		case "/v1/projects/" + project:
			data = map[string]any{"project": map[string]string{"id": project}}
		default:
			t.Errorf("unexpected backend request %s", r.URL)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": data})
	}))
	defer server.Close()
	if err := config.SetBackend(dir, models.BackendStoreConfig{URL: server.URL, ProjectID: project}, config.StoreSQLite); err != nil {
		t.Fatal(err)
	}
	m, err := NewBackendModelForWorktree(context.Background(), dir, dir, time.Second, "test")
	if err != nil {
		t.Fatal(err)
	}
	if m.DB != nil || m.SessionID != session || m.DataSource == nil || m.RefreshInterval != MinRefreshInterval || m.syncRuntime == nil || m.syncRuntime.service != nil {
		t.Fatal("backend monitor opened SQLite/sync or lost session")
	}
	provider, err := m.NotesFactory(m.NotesContext)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := provider.(*backendNoteStore); !ok {
		t.Fatal("backend monitor notes did not use the remote provider")
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if result := m.DataSource.Fetch("", false, SortByPriority); result.Error != context.Canceled {
		t.Fatalf("closed monitor accepted refresh: %v", result.Error)
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "todos.db")); err == nil {
		t.Fatal("backend monitor created local task database")
	}
}

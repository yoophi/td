package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/models"
	"github.com/spf13/cobra"
)

func TestBackendDeletedRoutesAndFilters(t *testing.T) {
	dir := storeTestDir(t)
	t.Setenv("TD_BACKEND_TOKEN", "deleted-fixture")
	t.Setenv("TD_BACKEND_CONTEXT_ROOT", t.TempDir())
	const project = "prj_0199dc38-1200-7000-8000-000000000001"
	const session = "ses_0199dc38-1200-7000-8000-000000000002"
	fail := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := "/v1/projects/" + project
		var data any
		switch r.URL.Path {
		case base + "/sessions":
			data = map[string]any{"session": map[string]string{"id": session}}
		case base:
			data = map[string]any{"project": map[string]any{"id": project}}
		case base + "/issues":
			if fail {
				w.WriteHeader(503)
				return
			}
			if r.URL.Query().Get("with_deleted") != "true" || r.URL.Query().Get("include_closed") != "true" || r.Header.Get("X-TD-Session") != session {
				t.Error("missing scope/deleted options")
			}
			data = map[string]any{"issues": []any{
				map[string]any{"id": "td-0000000000000001", "revision": "r-2", "title": "Deleted fixture", "deleted_at": "2026-10-14T00:00:00Z"},
				map[string]any{"id": "td-0000000000000002", "revision": "r-1", "title": "Live fixture"},
			}, "total": 2, "limit": 1000, "offset": 0, "has_more": false, "change_token": "stable"}
		default:
			t.Errorf("unexpected request %s", r.URL)
			w.WriteHeader(404)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": data})
	}))
	defer server.Close()
	if err := config.SetBackend(dir, models.BackendStoreConfig{URL: server.URL, ProjectID: project}, config.StoreSQLite); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		c := &cobra.Command{Use: "deleted", RunE: deletedCmd.RunE, SilenceUsage: true, SilenceErrors: true}
		c.Flags().Bool("json", false, "")
		c.SetOut(&out)
		c.SetArgs(args)
		err := c.Execute()
		return out.String(), err
	}
	got, err := run("--json")
	var issues []models.Issue
	if err != nil || json.Unmarshal([]byte(got), &issues) != nil || len(issues) != 1 || issues[0].DeletedAt == nil || issues[0].ID != "td-0000000000000001" {
		t.Fatal(got, err)
	}
	got, err = run()
	if err != nil || !strings.Contains(got, "Deleted fixture") || strings.Contains(got, "Live fixture") {
		t.Fatal(got, err)
	}
	fail = true
	if got, err = run("--json"); err == nil || got != "" {
		t.Fatal(fmt.Sprintf("read failure published data: %q", got), err)
	}
	if _, err = run("unexpected"); err == nil {
		t.Fatal("accepted positional argument")
	}
}

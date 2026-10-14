package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/models"
	"github.com/spf13/cobra"
)

func TestBackendUnstartSweepReportsMovedClaimSeparately(t *testing.T) {
	dir := storeTestDir(t)
	t.Setenv("TD_BACKEND_TOKEN", "sweep-fixture-token")
	t.Setenv("TD_BACKEND_CONTEXT_ROOT", t.TempDir())
	t.Setenv("TD_SESSION_ID", "sweep-fixture-context")
	const project = "prj_0199dc38-1200-7000-8000-000000000001"
	const current = "ses_0199dc38-1200-7000-8000-000000000002"
	const holder = "ses_0199dc38-1200-7000-8000-000000000003"
	ids := []string{"td-0000000000000001", "td-0000000000000002"}
	mutations := []string{}
	old := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339Nano)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := "/v1/projects/" + project
		var data any
		switch r.URL.Path {
		case base + "/sessions":
			if r.Method == "POST" {
				data = map[string]any{"session": map[string]string{"id": current}}
			} else {
				data = map[string]any{"current_session_id": current, "sessions": []any{map[string]string{"id": current, "last_activity": old}, map[string]string{"id": holder, "last_activity": old}}}
			}
		case base:
			data = map[string]any{"project": map[string]string{"id": project}}
		case base + "/changes":
			data = map[string]string{"change_token": "stable"}
		case base + "/issues":
			rows := []any{}
			for _, id := range ids {
				rows = append(rows, map[string]any{"id": id, "revision": "r-1", "status": "in_progress", "title": id, "implementer_session": holder, "updated_at": old})
			}
			data = map[string]any{"issues": rows, "total": 2, "limit": 1000, "offset": 0, "has_more": false, "change_token": "stable"}
		case base + "/issues/" + ids[0] + "/release":
			mutations = append(mutations, ids[0])
			w.WriteHeader(412)
			_, _ = w.Write([]byte(`{"ok":false,"error":{"code":"stale_revision"}}`))
			return
		case base + "/issues/" + ids[1] + "/release":
			mutations = append(mutations, ids[1])
			if r.Header.Get("If-Match") != `"r-1"` || r.Header.Get("Idempotency-Key") == "" || r.Header.Get("X-TD-Session") != current {
				t.Errorf("release headers: %v", r.Header)
			}
			var body backendReleaseInput
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Holder != holder || body.Mode != "session" {
				t.Errorf("release body: %+v, %v", body, err)
			}
			data = map[string]any{"issue": map[string]any{"id": ids[1], "revision": "r-2", "status": "open", "implementer_session": ""}}
		default:
			t.Errorf("unexpected backend path %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": data})
	}))
	defer server.Close()
	if err := config.SetBackend(dir, models.BackendStoreConfig{URL: server.URL, ProjectID: project}, config.StoreSQLite); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	cmd := &cobra.Command{Use: "unstart", RunE: unstartCmd.RunE, Args: unstartCmd.Args, SilenceUsage: true, SilenceErrors: true}
	cmd.Flags().String("session", "", "")
	cmd.Flags().String("stale", "", "")
	cmd.Flags().String("reason", "", "")
	cmd.Flags().Bool("force", false, "")
	cmd.Flags().Bool("json", false, "")
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--session", holder, "--force"})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Released 1, skipped 1, failed 0 claim(s).") || fmt.Sprint(mutations) != fmt.Sprint(ids) {
		t.Fatalf("out=%q mutations=%v", out.String(), mutations)
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatalf("backend sweep opened local SQLite: %v", err)
	}
}

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/models"
	"github.com/spf13/cobra"
)

func TestBackendDeleteRecoversLostResponseAndReason(t *testing.T) {
	dir := storeTestDir(t)
	t.Setenv("TD_BACKEND_TOKEN", "delete-fixture")
	t.Setenv("TD_BACKEND_CONTEXT_ROOT", t.TempDir())
	t.Setenv("TD_SESSION_ID", "delete-fixture-context")
	const project = "prj_0199dc38-1200-7000-8000-000000000001"
	const session = "ses_0199dc38-1200-7000-8000-000000000002"
	const id = "td-0000000000000001"
	deleted, commits, lost := false, 0, true
	keys := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := "/v1/projects/" + project
		var data any
		switch r.URL.Path {
		case base + "/sessions":
			data = map[string]any{"session": map[string]string{"id": session}}
		case base:
			data = map[string]any{"project": map[string]string{"id": project}}
		case base + "/issues":
			issue := map[string]any{"id": id, "revision": "r-1", "title": "Delete fixture"}
			if deleted {
				issue["revision"] = "r-2"
				issue["deleted_at"] = "2026-10-14T00:00:00Z"
			}
			data = map[string]any{"issues": []any{issue}, "total": 1, "limit": 1000, "offset": 0, "has_more": false, "change_token": fmt.Sprint(commits)}
		case base + "/issues/" + id:
			if r.Method != "DELETE" {
				t.Error("unexpected method", r.Method)
			}
			key := r.Header.Get("Idempotency-Key")
			keys = append(keys, key)
			raw, _ := io.ReadAll(r.Body)
			if string(raw) != `{"reason":"cleanup"}` {
				t.Error("reason changed", string(raw))
			}
			if commits == 0 {
				if r.Header.Get("If-Match") != `"r-1"` {
					t.Error("revision missing")
				}
				deleted = true
				commits++
			} else if len(keys) > 1 && keys[0] != key {
				t.Error("retry key changed")
			}
			if lost {
				lost = false
				io.WriteString(w, `{"ok":true,"data":`)
				return
			}
			data = map[string]any{"deleted": true, "issue": map[string]any{"id": id, "revision": "r-2", "title": "Delete fixture", "deleted_at": "2026-10-14T00:00:00Z"}}
		default:
			t.Errorf("unexpected path %s", r.URL)
			w.WriteHeader(404)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": data})
	}))
	defer server.Close()
	if e := config.SetBackend(dir, models.BackendStoreConfig{URL: server.URL, ProjectID: project}, config.StoreSQLite); e != nil {
		t.Fatal(e)
	}
	run := func() (string, error) {
		var out bytes.Buffer
		c := &cobra.Command{Use: "delete", RunE: deleteCmd.RunE, Args: deleteCmd.Args, SilenceUsage: true, SilenceErrors: true}
		c.Flags().String("reason", "", "")
		c.Flags().Bool("json", false, "")
		c.Flags().Bool("force", false, "")
		c.Flags().Bool("yes", false, "")
		c.SetOut(&out)
		c.SetArgs([]string{id, "--reason=cleanup", "--json"})
		e := c.ExecuteContext(context.Background())
		return out.String(), e
	}
	out, e := run()
	var uncertain *backendstore.UncertainWriteError
	if !errors.As(e, &uncertain) || out != "" || commits != 1 {
		t.Fatal(out, e, commits)
	}
	out, e = run()
	var results []struct {
		ID   string `json:"id"`
		Noop bool   `json:"noop"`
	}
	if e != nil || json.Unmarshal([]byte(out), &results) != nil || len(results) != 1 || results[0].ID != id || results[0].Noop || commits != 1 || len(keys) != 2 || keys[0] != keys[1] {
		t.Fatal(out, e, keys, commits)
	}
	out, e = run()
	if e != nil || json.Unmarshal([]byte(out), &results) != nil || len(results) != 1 || !results[0].Noop || commits != 1 || len(keys) != 2 {
		t.Fatal(out, e, keys, commits)
	}
}

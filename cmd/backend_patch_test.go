package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/models"
	"github.com/spf13/cobra"
)

func TestBackendPatchRecoversOriginalFileAfterLostResponse(t *testing.T) {
	dir := storeTestDir(t)
	t.Setenv("TD_BACKEND_TOKEN", "patch-fixture")
	t.Setenv("TD_BACKEND_CONTEXT_ROOT", t.TempDir())
	t.Setenv("TD_SESSION_ID", "patch-fixture-context")
	const project = "prj_0199dc38-1200-7000-8000-000000000001"
	const session = "ses_0199dc38-1200-7000-8000-000000000002"
	const id = "td-0000000000000001"
	original := "Original file content\n\nPreserved paragraph\n"
	file := filepath.Join(t.TempDir(), "description.md")
	if e := os.WriteFile(file, []byte(original), 0600); e != nil {
		t.Fatal(e)
	}
	commits := 0
	lost := true
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
			issue := map[string]any{"id": id, "revision": "r-1", "title": "Original title"}
			if commits > 0 {
				issue["revision"] = "r-2"
				issue["description"] = original
			}
			data = map[string]any{"issues": []any{issue}, "total": 1, "limit": 1000, "offset": 0, "has_more": false, "change_token": "stable"}
		case base + "/issues/" + id:
			if r.Method != "PATCH" {
				t.Error("unexpected method", r.Method)
			}
			keys = append(keys, r.Header.Get("Idempotency-Key"))
			if r.Header.Get("If-Match") != `"r-1"` {
				t.Error("lost observed revision")
			}
			raw, _ := io.ReadAll(r.Body)
			var body map[string]any
			if json.Unmarshal(raw, &body) != nil || body["description"] != original {
				t.Error("payload changed", string(raw))
			}
			if commits == 0 {
				commits++
			} else if keys[0] != keys[1] {
				t.Error("retry key changed")
			}
			if lost {
				lost = false
				io.WriteString(w, `{"ok":true,"data":`)
				return
			}
			data = map[string]any{"issue": map[string]any{"id": id, "revision": "r-2", "title": "Original title", "description": original}}
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
		c := &cobra.Command{Use: "update", RunE: updateCmd.RunE, Args: updateCmd.Args, SilenceUsage: true, SilenceErrors: true}
		for _, name := range []string{"description", "desc", "body", "acceptance", "acceptance-file"} {
			c.Flags().String(name, "", "")
		}
		c.Flags().String("description-file", "", "")
		c.Flags().Bool("append", false, "")
		c.Flags().String("status", "", "")
		c.Flags().Bool("json", false, "")
		c.SetOut(&out)
		c.SetArgs([]string{id, "--description-file=" + file, "--json"})
		e := c.ExecuteContext(context.Background())
		return out.String(), e
	}
	out, e := run()
	var uncertain *backendstore.UncertainWriteError
	if !errors.As(e, &uncertain) || out != "" || commits != 1 {
		t.Fatal(out, e, commits)
	}
	if e := os.WriteFile(file, []byte("Changed after lost reply"), 0600); e != nil {
		t.Fatal(e)
	}
	out, e = run()
	if e != nil || commits != 1 || len(keys) != 2 || keys[0] != keys[1] {
		t.Fatal(out, e, keys, commits)
	}
	var result struct {
		Issue models.Issue `json:"issue"`
	}
	if json.Unmarshal([]byte(out), &result) != nil || result.Issue.Description != original {
		t.Fatal(out, e)
	}
}

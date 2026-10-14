package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/models"
	"github.com/spf13/cobra"
)

func TestBackendLogRecoversOriginalStdinAfterLostResponse(t *testing.T) {
	dir := storeTestDir(t)
	t.Setenv("TD_BACKEND_TOKEN", "log-fixture")
	t.Setenv("TD_BACKEND_CONTEXT_ROOT", t.TempDir())
	t.Setenv("TD_SESSION_ID", "log-fixture-context")
	const project = "prj_0199dc38-1200-7000-8000-000000000001"
	const session = "ses_0199dc38-1200-7000-8000-000000000002"
	const id = "td-0000000000000001"
	original := "Original stdin log body"
	commits, lost := 0, true
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
			data = map[string]any{"issues": []any{map[string]string{"id": id, "revision": "r-1"}}, "total": 1, "limit": 1000, "offset": 0, "has_more": false, "change_token": "stable"}
		case base + "/work-sessions":
			data = map[string]any{"work_sessions": []any{}}
		case base + "/issues/" + id + "/logs":
			keys = append(keys, r.Header.Get("Idempotency-Key"))
			raw, _ := io.ReadAll(r.Body)
			var body map[string]any
			if json.Unmarshal(raw, &body) != nil || body["message"] != original {
				t.Error("immutable stdin body changed", string(raw))
			}
			if r.Header.Get("If-Match") != `"r-1"` {
				t.Error("missing observed revision")
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
			data = map[string]any{"log": map[string]any{"id": "lg-0000000000000001", "issue_id": id, "session_id": session, "message": original, "type": "progress", "timestamp": "2026-10-14T00:00:00Z"}}
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
	run := func(stdin string) (string, error) {
		var out bytes.Buffer
		c := &cobra.Command{Use: "log", RunE: logCmd.RunE, Args: logCmd.Args, SilenceUsage: true, SilenceErrors: true}
		c.Flags().String("issue", "", "")
		c.Flags().String("task", "", "")
		c.Flags().String("type", "", "")
		for _, name := range []string{"blocker", "decision", "hypothesis", "tried", "result", "json"} {
			c.Flags().Bool(name, false, "")
		}
		c.SetIn(bytes.NewBufferString(stdin))
		c.SetOut(&out)
		c.SetArgs([]string{id, "--json"})
		e := c.ExecuteContext(context.Background())
		return out.String(), e
	}
	out, e := run(original)
	var uncertain *backendstore.UncertainWriteError
	if !errors.As(e, &uncertain) || out != "" || commits != 1 {
		t.Fatal(out, e, commits)
	}
	out, e = run("Changed stdin after lost reply")
	if e != nil || len(keys) != 2 || keys[0] != keys[1] || commits != 1 {
		t.Fatal(out, e, keys, commits)
	}
	var result struct {
		Log struct {
			Message string `json:"message"`
		} `json:"log"`
	}
	if json.Unmarshal([]byte(out), &result) != nil || result.Log.Message != original {
		t.Fatal(out, e)
	}
}

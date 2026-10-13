package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/models"
	"github.com/spf13/cobra"
)

func TestBackendShowDetailSelectionAndOutput(t *testing.T) {
	dir := storeTestDir(t)
	t.Setenv("TD_BACKEND_TOKEN", "show-fixture-token")
	t.Setenv("TD_BACKEND_CONTEXT_ROOT", t.TempDir())
	t.Setenv("TD_SESSION_ID", "show-context")
	const project = "prj_0199dc38-1200-7000-8000-000000000001"
	const session = "ses_0199dc38-1200-7000-8000-000000000002"
	const parent = "td-0123456789abcdef"
	const child = "td-9876543210abcdef"
	rows := []map[string]any{
		{"id": parent, "revision": "r-2", "title": "Remote parent", "status": "in_progress", "type": "epic", "priority": "P1"},
		{"id": child, "revision": "r-1", "title": "Remote child", "status": "open", "type": "task", "priority": "P2", "parent_id": parent},
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var data any
		path := "/v1/projects/" + project
		switch r.URL.Path {
		case path + "/sessions":
			data = map[string]any{"session": map[string]string{"id": session}}
		case path:
			data = map[string]any{"project": map[string]string{"id": project}}
		case path + "/focus":
			data = map[string]any{"focused_issue_id": parent}
		case path + "/issues":
			data = map[string]any{"issues": rows, "limit": 1000, "offset": 0, "total": 2, "has_more": false, "change_token": "current"}
		case path + "/issues/" + parent:
			if r.URL.Query().Get("with") != "reviews" {
				t.Error("review history not requested")
			}
			data = map[string]any{"issue": rows[0], "logs": []any{}, "children": []any{rows[1]}, "files": []any{}, "dependencies": []any{}, "blocked_by": []any{}, "comments": []any{}}
		case path + "/issues/" + child:
			data = map[string]any{"issue": rows[1], "logs": []any{}, "children": []any{}, "files": []any{}, "dependencies": []any{}, "blocked_by": []any{}, "comments": []any{}}
		default:
			w.WriteHeader(404)
			return
		}
		if r.Method != "POST" && r.Header.Get("X-TD-Session") != session {
			t.Error("missing caller")
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": data})
	}))
	defer server.Close()
	if e := config.SetBackend(dir, models.BackendStoreConfig{URL: server.URL, ProjectID: project}, config.StoreSQLite); e != nil {
		t.Fatal(e)
	}
	run := func(args ...string) (string, error) {
		var output bytes.Buffer
		c := &cobra.Command{Use: "show", RunE: showCmd.RunE, Args: showCmd.Args, SilenceUsage: true, SilenceErrors: true}
		c.Flags().String("format", "", "")
		c.Flags().Bool("json", false, "")
		c.Flags().Bool("tree", false, "")
		c.Flags().Bool("children", false, "")
		c.Flags().Bool("short", false, "")
		c.Flags().Bool("render-markdown", false, "")
		c.SetOut(&output)
		c.SetArgs(args)
		e := c.ExecuteContext(context.Background())
		return output.String(), e
	}
	for _, args := range [][]string{{parent, "--json"}, {"012345", "--json"}, {"--json"}, {parent, "--children", "--format=json"}, {parent, "--tree", "--json"}} {
		output, e := run(args...)
		if e != nil {
			t.Fatal(args, e)
		}
		var decoded map[string]any
		if e = json.Unmarshal([]byte(output), &decoded); e != nil || decoded["id"] != parent || decoded["revision"] != "r-2" {
			t.Fatal(output, e)
		}
		if strings.Contains(strings.Join(args, " "), "--children") || strings.Contains(strings.Join(args, " "), "--tree") {
			if children, ok := decoded["children"].([]any); !ok || len(children) != 1 {
				t.Fatal("children missing", decoded)
			}
		}
	}
	output, e := run(parent, child, "--json")
	var multi []any
	if e != nil || json.Unmarshal([]byte(output), &multi) != nil || len(multi) != 2 {
		t.Fatal(output, e)
	}
	output, e = run(parent, "--short")
	if e != nil || !strings.Contains(output, "Remote parent") {
		t.Fatal(output, e)
	}
	output, e = run(parent, "td-0000000000000000", "--json")
	if e == nil || output != "" {
		t.Fatal("partial failure printed as success", output, e)
	}
	before := requests
	if _, e = run(parent, "--format=invalid"); e == nil || requests != before {
		t.Fatal("invalid format sent request", e)
	}
}

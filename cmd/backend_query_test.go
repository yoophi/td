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

func TestBackendQueryPredicateActivityAndScanLimit(t *testing.T) {
	dir := storeTestDir(t)
	t.Setenv("TD_BACKEND_TOKEN", "query-fixture-token")
	t.Setenv("TD_BACKEND_CONTEXT_ROOT", t.TempDir())
	t.Setenv("TD_SESSION_ID", "query-context")
	const project = "prj_0199dc38-1200-7000-8000-000000000001"
	const session = "ses_0199dc38-1200-7000-8000-000000000002"
	const first = "td-0000000000000001"
	const second = "td-0000000000000002"
	rows := []map[string]any{
		{"id": first, "revision": "r-1", "title": "First fixture", "status": "open", "type": "task", "priority": "P1", "implementer_session": session},
		{"id": second, "revision": "r-1", "title": "Second fixture", "status": "closed", "type": "bug", "priority": "P2"},
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
		case path + "/changes":
			data = map[string]string{"change_token": "same"}
		case path + "/issues":
			data = map[string]any{"issues": rows, "total": 2, "limit": 1000, "offset": 0, "has_more": false, "change_token": "same"}
		case path + "/query":
			data = map[string]any{"ids": []string{first}}
		case path + "/issues/" + first, path + "/issues/" + second:
			row := rows[0]
			comments := []any{map[string]any{"id": "c1", "issue_id": first, "text": "ready", "created_at": "2026-10-13T00:00:00Z"}}
			if strings.HasSuffix(r.URL.Path, second) {
				row = rows[1]
				comments = []any{}
			}
			data = map[string]any{"issue": row, "comments": comments, "logs": []any{}, "children": []any{}, "files": []any{}, "dependencies": []any{}}
		default:
			w.WriteHeader(404)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": data})
	}))
	defer server.Close()
	if e := config.SetBackend(dir, models.BackendStoreConfig{URL: server.URL, ProjectID: project}, config.StoreSQLite); e != nil {
		t.Fatal(e)
	}
	run := func(expression string, flags ...string) (string, string, error) {
		var out, errOut bytes.Buffer
		c := &cobra.Command{Use: "query", RunE: queryCmd.RunE, Args: queryCmd.Args, SilenceErrors: true, SilenceUsage: true}
		c.Flags().String("output", "table", "")
		c.Flags().String("sort", "", "")
		c.Flags().Int("limit", 0, "")
		c.Flags().Int("max-scan", 0, "")
		for _, f := range []string{"json", "fields", "examples", "explain"} {
			c.Flags().Bool(f, false, "")
		}
		c.SetOut(&out)
		c.SetErr(&errOut)
		c.SetArgs(append([]string{expression}, flags...))
		e := c.ExecuteContext(context.Background())
		return out.String(), errOut.String(), e
	}
	for _, expression := range []string{"status = open", "comment.text ~ ready", "rework()", "implementer = @me"} {
		out, _, e := run(expression, "--json")
		if e != nil {
			t.Fatal(expression, e)
		}
		var got []models.Issue
		if e = json.Unmarshal([]byte(out), &got); e != nil || len(got) != 1 || got[0].ID != first {
			t.Fatal(expression, out, e)
		}
	}
	out, _, e := run("status != blocked", "--output=ids")
	if e != nil || !strings.Contains(out, first) || !strings.Contains(out, second) {
		t.Fatal(out, e)
	}
	out, warning, e := run("status != blocked", "--max-scan=1", "--output=count")
	if e != nil || strings.TrimSpace(out) != "1" || !strings.Contains(warning, "first 1") {
		t.Fatal(out, warning, e)
	}
	before := requests
	if _, _, e = run("unknown_field = value", "--json"); e == nil || requests != before {
		t.Fatal("invalid query reached server", e)
	}
}

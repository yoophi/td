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
		{"id": first, "revision": "r-1", "title": "First fixture", "status": "open", "type": "task", "priority": "P1", "points": 3, "labels": []string{"api"}, "implementer_session": session},
		{"id": second, "revision": "r-1", "title": "Billing fixture", "status": "closed", "type": "bug", "priority": "P2", "points": 5, "parent_id": first},
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
		case path + "/stats":
			data = map[string]any{"total": 2, "by_status": map[string]int{"open": 1, "closed": 1}, "by_type": map[string]int{"task": 1, "bug": 1}, "by_priority": map[string]int{"P1": 1, "P2": 1}}
		case path + "/changes":
			data = map[string]string{"change_token": "same"}
		case path + "/issues":
			data = map[string]any{"issues": rows, "total": 2, "limit": 1000, "offset": 0, "has_more": false, "change_token": "same"}
		case path + "/focus":
			data = map[string]any{"focused_issue_id": first}
		case path + "/monitor":
			data = map[string]any{"session_id": session, "monitor": map[string]any{"task_list": map[string]any{"review_verification_required": false, "reviewable": []any{rows[0]}, "ready_to_close": []any{rows[1]}}}}
		case path + "/query":
			data = map[string]any{"ids": []string{first}}
		case path + "/issues/" + first, path + "/issues/" + second:
			row := rows[0]
			comments := []any{map[string]any{"id": "c1", "issue_id": first, "text": "ready", "created_at": "2026-10-13T00:00:00Z"}}
			if strings.HasSuffix(r.URL.Path, second) {
				row = rows[1]
				comments = []any{}
			}
			logs := []any{}
			if row["id"] == first {
				logs = []any{map[string]any{"id": "l1", "issue_id": first, "message": "billing completion", "type": "progress", "timestamp": "2026-10-13T00:00:00Z"}}
			}
			data = map[string]any{"issue": row, "comments": comments, "logs": logs, "children": []any{}, "files": []any{}, "dependencies": []any{}}
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
	runSearch := func(text string, flags ...string) (string, error) {
		var out bytes.Buffer
		c := &cobra.Command{Use: "search", RunE: searchCmd.RunE, Args: searchCmd.Args, SilenceErrors: true, SilenceUsage: true}
		for _, name := range []string{"status", "type", "labels"} {
			c.Flags().StringArray(name, nil, "")
		}
		c.Flags().String("priority", "", "")
		c.Flags().Int("limit", 50, "")
		c.Flags().Bool("json", false, "")
		c.Flags().Bool("show-score", false, "")
		c.SetOut(&out)
		c.SetArgs(append([]string{text}, flags...))
		e := c.ExecuteContext(context.Background())
		return out.String(), e
	}
	searchJSON, e := runSearch("billing", "--json")
	var ranked []struct {
		Issue      models.Issue `json:"issue"`
		Score      int          `json:"score"`
		MatchField string       `json:"MatchField"`
	}
	if e != nil || json.Unmarshal([]byte(searchJSON), &ranked) != nil || len(ranked) != 2 || ranked[0].Issue.ID != second || ranked[1].MatchField != "log" {
		t.Fatal("ranked search", searchJSON, e)
	}
	searchJSON, e = runSearch("ready", "--json")
	if e != nil || strings.TrimSpace(searchJSON) != "[]" {
		t.Fatal("comments searched as activity", searchJSON, e)
	}
	searchText, e := runSearch("billing", "--status=open", "--show-score")
	if e != nil || !strings.Contains(searchText, first) || strings.Contains(searchText, second) || !strings.Contains(searchText, "score:10 log") {
		t.Fatal(searchText, e)
	}
	runList := func(args ...string) (string, error) {
		var out bytes.Buffer
		c := &cobra.Command{Use: "list", RunE: listCmd.RunE, Args: listCmd.Args, SilenceUsage: true, SilenceErrors: true}
		for _, name := range []string{"status", "type", "labels", "id"} {
			c.Flags().StringArray(name, nil, "")
		}
		for _, name := range []string{"priority", "search", "parent", "epic", "points", "created", "updated", "closed", "implementer", "reviewer", "filter", "format"} {
			c.Flags().String(name, "", "")
		}
		c.Flags().String("sort", "priority", "")
		c.Flags().Int("limit", 0, "")
		for _, name := range []string{"json", "all", "open", "long", "short", "reverse", "mine", "reviewable", "include-approved", "deferred", "overdue", "due-soon", "surfacing"} {
			c.Flags().Bool(name, false, "")
		}
		c.SetOut(&out)
		c.SetArgs(args)
		e := c.ExecuteContext(context.Background())
		return out.String(), e
	}
	for _, tc := range []struct {
		flags []string
		ids   []string
	}{
		{nil, []string{first}}, {[]string{"--all"}, []string{first, second}},
		{[]string{"--all", "--type=bug"}, []string{second}},
		{[]string{"--mine"}, []string{first}}, {[]string{"--points=1-4"}, []string{first}},
		{[]string{"--labels=api"}, []string{first}}, {[]string{"--labels=missing"}, []string{}},
		{[]string{"--all", "--parent=" + first}, []string{second}},
		{[]string{"--all", "--epic=."}, []string{second}},
		{[]string{"--all", "--search=Billing"}, []string{second}},
		{[]string{"--all", "--sort=points", "--reverse", "--limit=1"}, []string{second}},
		{[]string{"--filter=comment.text ~ ready"}, []string{first}},
		{[]string{"--reviewable"}, []string{first}},
	} {
		out, e := runList(append(tc.flags, "--json")...)
		if e != nil {
			t.Fatal(tc.flags, e)
		}
		var got []models.Issue
		if e = json.Unmarshal([]byte(out), &got); e != nil || len(got) != len(tc.ids) {
			t.Fatal(tc.flags, out, e)
		}
		for i, id := range tc.ids {
			if got[i].ID != id {
				t.Fatal(tc.flags, out)
			}
		}
	}
	long, e := runList("--long")
	if e != nil || !strings.Contains(long, "billing completion") {
		t.Fatal("long list omitted activity", long, e)
	}
	listBefore := requests
	if _, e = runList("--type=invalid", "--json"); e == nil || requests != listBefore {
		t.Fatal("invalid list filter sent request", e)
	}
	var infoOut bytes.Buffer
	info := &cobra.Command{Use: "info", RunE: infoCmd.RunE, Args: infoCmd.Args}
	info.Flags().Bool("json", true, "")
	info.SetOut(&infoOut)
	if e := info.ExecuteContext(context.Background()); e != nil {
		t.Fatal(e)
	}
	var overview map[string]any
	if e := json.Unmarshal(infoOut.Bytes(), &overview); e != nil || overview["store"] != "td-backend" || overview["current_session"] != session || overview["database"] != nil {
		t.Fatal(infoOut.String(), e)
	}
	if issues, ok := overview["issues"].(map[string]any); !ok || issues["total"] != float64(2) {
		t.Fatal(overview)
	}
	before := requests
	if _, _, e = run("unknown_field = value", "--json"); e == nil || requests != before {
		t.Fatal("invalid query reached server", e)
	}
}

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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/models"
	"github.com/spf13/cobra"
)

func TestBackendCreateRecoversSameRequestAndPreservesFields(t *testing.T) {
	dir := storeTestDir(t)
	t.Setenv("TD_BACKEND_TOKEN", "create-fixture-token")
	t.Setenv("TD_BACKEND_CONTEXT_ROOT", t.TempDir())
	t.Setenv("TD_SESSION_ID", "create-fixture-context")
	const project = "prj_0199dc38-1200-7000-8000-000000000001"
	const session = "ses_0199dc38-1200-7000-8000-000000000002"
	const parent = "td-00000000000000a1"
	const dependency = "td-00000000000000b2"
	created, requests := 0, 0
	lost := true
	supportBlocks := true
	bodies := map[string]string{}
	replies := map[string]map[string]any{}
	keys := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Authorization") != "Bearer create-fixture-token" {
			t.Error("missing credential")
		}
		base := "/v1/projects/" + project
		var data any
		switch r.URL.Path {
		case "/v1/capabilities":
			caps := []string{}
			if supportBlocks {
				caps = append(caps, "create_blocks")
			}
			data = map[string]any{"api_version": "v1", "capabilities": caps}
		case base + "/sessions":
			data = map[string]any{"session": map[string]string{"id": session}}
		case base:
			data = map[string]any{"project": map[string]any{"id": project, "title_min_length": 15, "title_max_length": 200}}
		case base + "/changes":
			data = map[string]string{"change_token": "stable"}
		case base + "/issues":
			if r.Method == "GET" {
				data = map[string]any{"issues": []any{map[string]string{"id": parent, "revision": "r-1"}, map[string]string{"id": dependency, "revision": "r-1"}}, "total": 2, "limit": 1000, "offset": 0, "has_more": false, "change_token": "stable"}
				break
			}
			if r.Header.Get("X-TD-Session") != session {
				t.Error("missing attribution")
			}
			key := r.Header.Get("Idempotency-Key")
			keys = append(keys, key)
			raw, _ := io.ReadAll(r.Body)
			if prior, ok := bodies[key]; ok && prior != string(raw) {
				t.Error("retry changed payload")
			}
			if replies[key] == nil {
				created++
				bodies[key] = string(raw)
				var body map[string]any
				if json.Unmarshal(raw, &body) != nil {
					t.Fatal("invalid body")
				}
				body["id"] = fmt.Sprintf("td-%016x", created)
				body["revision"] = "r-1"
				body["creator_session"] = session
				body["status"] = "open"
				replies[key] = body
			}
			if lost {
				lost = false
				io.WriteString(w, `{"ok":true,"data":`)
				return
			}
			data = map[string]any{"issue": replies[key]}
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
	description := filepath.Join(t.TempDir(), "description.md")
	original := "Original paragraph\n\n  preserved indentation\n"
	if e := os.WriteFile(description, []byte(original), 0600); e != nil {
		t.Fatal(e)
	}
	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		c := &cobra.Command{Use: "create", RunE: createCmd.RunE, Args: createCmd.Args, SilenceUsage: true, SilenceErrors: true}
		registerCreateFlags(c)
		c.Flags().Bool("json", false, "")
		c.SetOut(&out)
		c.SetArgs(args)
		e := c.ExecuteContext(context.Background())
		return out.String(), e
	}
	args := []string{"Remote creation fixture", "--type=story", "--priority=p1", "--points=3", "--labels=api,client", "--minor", "--description-file=" + description, "--acceptance=works", "--due=2026-12-31", "--parent=00000000000000a", "--depends-on=00000000000000b"}
	output, e := run(args...)
	var uncertain *backendstore.UncertainWriteError
	if !errors.As(e, &uncertain) || output != "" {
		t.Fatal("lost response reported successful", output, e)
	}
	if e := os.WriteFile(description, []byte("changed after response loss"), 0600); e != nil {
		t.Fatal(e)
	}
	output, e = run(append(args, "--json")...)
	if e != nil {
		t.Fatal(e)
	}
	var result struct {
		ID    string       `json:"id"`
		Issue models.Issue `json:"issue"`
	}
	if e = json.Unmarshal([]byte(output), &result); e != nil || result.ID != "td-0000000000000001" || result.Issue.Description != original || result.Issue.Type != models.TypeFeature || result.Issue.Priority != models.PriorityP1 || result.Issue.Points != 3 || !result.Issue.Minor || result.Issue.ParentID != parent || result.Issue.CreatorSession != session {
		t.Fatal(output, e)
	}
	var payload map[string]any
	json.Unmarshal([]byte(bodies[keys[0]]), &payload)
	if ids, ok := payload["depends_on"].([]any); !ok || len(ids) != 1 || ids[0] != dependency {
		t.Fatal(payload)
	}
	if created != 1 || len(keys) != 2 || keys[0] != keys[1] {
		t.Fatal("duplicate after retry", created, keys)
	}
	if _, e = run(append(args, "--json")...); e != nil || created != 2 || keys[2] == keys[1] {
		t.Fatal("new invocation not fresh", e, keys)
	}
	blockOutput, e := run("Remote incoming block fixture", "--blocks="+parent, "--json")
	if e != nil {
		t.Fatal(blockOutput, e)
	}
	var lastBody map[string]any
	json.Unmarshal([]byte(bodies[keys[len(keys)-1]]), &lastBody)
	if observations, ok := lastBody["blocks"].([]any); !ok || len(observations) != 1 || observations[0].(map[string]any)["issue_id"] != parent || observations[0].(map[string]any)["revision"] != "r-1" {
		t.Fatal(lastBody)
	}
	supportBlocks = false
	blockCount := created
	if _, e = run("Unsupported backend block fixture", "--blocks="+parent); e == nil || created != blockCount {
		t.Fatal("unsupported server received create", e, created)
	}
	before := requests
	if _, e = run("Remote invalid type fixture", "--type=invalid"); e == nil || requests != before {
		t.Fatal("invalid type reached server", e)
	}
	if _, e = os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(e) {
		t.Fatal("SQLite fallback", e)
	}
	if !strings.Contains(output, "created") {
		t.Fatal(output)
	}
}

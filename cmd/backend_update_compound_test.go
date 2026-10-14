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
	"strconv"
	"testing"

	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/models"
	"github.com/spf13/cobra"
)

func TestBackendCompoundUpdateRecoversFrozenFieldAndComment(t *testing.T) {
	dir := storeTestDir(t)
	t.Setenv("TD_BACKEND_TOKEN", "compound-fixture")
	t.Setenv("TD_BACKEND_CONTEXT_ROOT", t.TempDir())
	t.Setenv("TD_SESSION_ID", "compound-fixture-context")
	const project = "prj_0199dc38-1200-7000-8000-000000000031"
	const session = "ses_0199dc38-1200-7000-8000-000000000032"
	const id = "td-0000000000000031"
	file := filepath.Join(t.TempDir(), "description.md")
	if e := os.WriteFile(file, []byte("Frozen description"), 0600); e != nil {
		t.Fatal(e)
	}
	revision := 1
	writes := map[string]int{}
	var commentKey string
	lost := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := "/v1/projects/" + project
		data := any(nil)
		switch r.URL.Path {
		case base + "/sessions":
			data = map[string]any{"session": map[string]string{"id": session}}
		case base:
			data = map[string]any{"project": map[string]string{"id": project}}
		case base + "/issues":
			data = map[string]any{"issues": []any{map[string]any{"id": id, "title": "Initial", "revision": "r-1"}}, "total": 1, "limit": 1000, "offset": 0, "has_more": false, "change_token": "stable"}
		case base + "/issues/" + id:
			if r.Method == "GET" {
				data = map[string]any{"issue": map[string]any{"id": id, "title": "Initial", "revision": revisionString(revision)}}
				break
			}
			if r.Method != "PATCH" {
				t.Errorf("method %s", r.Method)
			}
			key := r.Header.Get("Idempotency-Key")
			writes[key]++
			raw, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			if body["description"] != "Frozen description" {
				t.Errorf("field changed: %s", raw)
			}
			if writes[key] == 1 {
				revision++
			}
			data = map[string]any{"issue": map[string]any{"id": id, "title": "Initial", "description": "Frozen description", "revision": revisionString(revision)}}
		case base + "/issues/" + id + "/comments":
			key := r.Header.Get("Idempotency-Key")
			if commentKey == "" {
				commentKey = key
			} else if commentKey != key {
				t.Error("comment retry changed key")
			}
			writes[key]++
			raw, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			if body["text"] != "Once" {
				t.Errorf("comment changed: %s", raw)
			}
			if writes[key] == 1 {
				revision++
			}
			if lost {
				lost = false
				_, _ = io.WriteString(w, `{"ok":true,"data":`)
				return
			}
			data = map[string]any{"comment": map[string]any{"id": "act-comment", "issue_id": id, "session_id": session, "text": "Once", "created_at": "2026-10-14T00:00:00Z"}}
		default:
			t.Errorf("unexpected path %s", r.URL)
			w.WriteHeader(404)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": data})
	}))
	defer server.Close()
	if e := config.SetBackend(dir, models.BackendStoreConfig{URL: server.URL, ProjectID: project}, config.StoreSQLite); e != nil {
		t.Fatal(e)
	}
	run := func() error {
		var out bytes.Buffer
		c := &cobra.Command{Use: "update", RunE: updateCmd.RunE, Args: updateCmd.Args, SilenceUsage: true, SilenceErrors: true}
		c.Flags().String("description-file", "", "")
		c.Flags().String("description", "", "")
		c.Flags().String("desc", "", "")
		c.Flags().String("body", "", "")
		c.Flags().String("acceptance", "", "")
		c.Flags().String("acceptance-file", "", "")
		c.Flags().String("comment", "", "")
		c.Flags().String("note", "", "")
		c.Flags().Bool("append", false, "")
		c.Flags().Bool("json", false, "")
		c.SetOut(&out)
		c.SetArgs([]string{id, "--description-file=" + file, "--comment=Once", "--json"})
		e := c.ExecuteContext(context.Background())
		if e == nil && !bytes.Contains(out.Bytes(), []byte(`"action":"updated"`)) {
			t.Error("missing update output", out.String())
		}
		return e
	}
	var uncertain *backendstore.UncertainWriteError
	if e := run(); !errors.As(e, &uncertain) {
		t.Fatalf("expected uncertain comment response: %v", e)
	}
	if e := os.WriteFile(file, []byte("Changed after first write"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := run(); e != nil {
		t.Fatal(e)
	}
	if len(writes) != 2 || writes[commentKey] != 2 || revision != 3 {
		t.Fatal("replayed a step as a new write", writes, revision)
	}
}

func revisionString(value int) string { return "r-" + strconv.Itoa(value) }

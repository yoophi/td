package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/db"
	"github.com/spf13/cobra"
)

func TestInitBackendCommandAndReuse(t *testing.T) {
	dir := storeTestDir(t)
	t.Setenv("TD_BACKEND_CONTEXT_ROOT", t.TempDir())
	t.Setenv("TD_BACKEND_TOKEN", "cli-backend-test-credential-only")
	const p = "prj_0199dc38-1200-7000-8000-000000000001"
	const session = "ses_0199dc38-1200-7000-8000-000000000002"
	created := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var data any
		switch r.URL.Path {
		case "/healthz":
			data = map[string]string{"status": "ok", "api_version": "v1"}
		case "/v1/capabilities":
			data = map[string]any{"api_version": "v1", "capabilities": []string{"projects", "sessions", "idempotent_init"}}
		case "/v1/projects":
			created++
			data = map[string]any{"project": map[string]string{"id": p}}
		case "/v1/projects/" + p + "/sessions":
			data = map[string]any{"session": map[string]string{"id": session}}
		case "/v1/projects/" + p:
			if r.Header.Get("X-TD-Session") != session {
				w.WriteHeader(403)
				return
			}
			data = map[string]any{"project": map[string]string{"id": p}}
		default:
			w.WriteHeader(404)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": data})
	}))
	defer server.Close()
	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		cmd := &cobra.Command{Use: "init", RunE: initCmd.RunE, Args: initCmd.Args, SilenceErrors: true, SilenceUsage: true}
		cmd.Flags().String("store", "sqlite", "")
		cmd.Flags().String("remote", "origin", "")
		cmd.Flags().String("project-id", "", "")
		cmd.Flags().Bool("json", false, "")
		cmd.SetOut(&out)
		cmd.SetArgs(args)
		e := cmd.ExecuteContext(context.Background())
		return out.String(), e
	}
	output, e := run("--store=td-backend,"+server.URL, "--json")
	if e != nil {
		t.Fatal(e)
	}
	var decoded map[string]any
	if e = json.Unmarshal([]byte(output), &decoded); e != nil || decoded["store"] != config.StoreBackend || decoded["backend"] == nil || decoded["issue_operations_supported"] != false {
		t.Fatal(output, e)
	}
	if _, e = run("--json"); e != nil {
		t.Fatal(e)
	}
	var setupOutput bytes.Buffer
	setup := &cobra.Command{Use: "setup", RunE: configSetupCmd.RunE}
	setup.Flags().Bool("json", false, "")
	setup.SetIn(strings.NewReader("3\n" + server.URL + "\n"))
	setup.SetOut(&setupOutput)
	if e = setup.ExecuteContext(context.Background()); e != nil {
		t.Fatal(e)
	}
	var listOutput bytes.Buffer
	list := &cobra.Command{Use: "list", RunE: configListCmd.RunE}
	list.SetOut(&listOutput)
	if e = list.ExecuteContext(context.Background()); e != nil {
		t.Fatal(e)
	}
	var settings map[string]any
	if e = json.Unmarshal(listOutput.Bytes(), &settings); e != nil || settings["backend"] == nil {
		t.Fatal("backend config not listed", listOutput.String(), e)
	}
	if created != 1 {
		t.Fatal("repeat init creates project", created)
	}
	if _, e = os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(e) {
		t.Fatal("SQLite created", e)
	}
	if database, e := db.Open(dir); e == nil {
		database.Close()
		t.Fatal("backend silently opened SQLite")
	}
	for _, tc := range []struct {
		command *cobra.Command
		args    []string
	}{
		{listCmd, nil},

		{startCmd, []string{"td-0123456789abcdef"}},
		{closeCmd, []string{"td-0123456789abcdef"}},
	} {
		previous := tc.command.Context()
		tc.command.SetContext(context.Background())
		err := tc.command.RunE(tc.command, tc.args)
		tc.command.SetContext(previous)
		if err == nil || !strings.Contains(err.Error(), "td-backend") {
			t.Fatalf("%s routed outside backend guard: %v", tc.command.Name(), err)
		}
	}
	if _, e = run("--project-id=invalid", "--json"); e == nil {
		t.Fatal("invalid project accepted")
	}
	if _, e = run("--remote=origin", "--json"); e == nil {
		t.Fatal("GitHub remote flag accepted")
	}
}

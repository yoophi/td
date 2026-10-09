package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/query"
)

func TestOfflineQueryHelperCLIJSONAndNoNetwork(t *testing.T) {
	dir := githubScheduleTestDir(t)
	calls := recordScheduleGHCalls(t)
	t.Setenv("TD_GH_SYNC_HELPER", "1")
	t.Setenv("TD_GH_CONTEXT_ROOT", t.TempDir())
	t.Setenv("TD_FEATURE_SYNC_AUTOSYNC", "true")
	t.Setenv("TD_ANALYTICS", "false")
	ghPath := filepath.Join(strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0], "gh")
	if err := os.WriteFile(ghPath, []byte("#!/bin/sh\nprintf 'unexpected gh call\\n' >> \"$TD_SYNC_STATUS_CALLS\"\nexit 9\n"), 0755); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []string{"false", "true"} {
		t.Setenv("TD_FEATURE_SYNC_CLI", enabled)
		for _, tc := range []struct {
			args []string
			want string
			fail bool
		}{
			{[]string{"--fields"}, `"cross_entity_fields":`, false},
			{[]string{"--examples"}, `"examples":`, false},
			{[]string{"due <= today", "--explain"}, `"executed":false`, false},
			{[]string{"--explain"}, `"error":`, true},
			{[]string{"unknown_field = x", "--explain"}, `"error":`, true},
			{[]string{"status =", "--explain"}, `"error":`, true},
			{[]string{"--fields", "--examples"}, `"error":`, true},
			{[]string{"status = open", "--fields"}, `"error":`, true},
			{[]string{"--examples", "--limit", "2"}, `"error":`, true},
		} {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			args := append([]string{"-test.run=^TestGitHubSyncCLIHelperProcess$", "--", "--work-dir", dir, "--json", "query"}, tc.args...)
			child := exec.CommandContext(ctx, executable, args...)
			var stdout, stderr bytes.Buffer
			child.Stdout = &stdout
			child.Stderr = &stderr
			err := child.Run()
			cancel()
			if (err != nil) != tc.fail || !json.Valid(stdout.Bytes()) || !strings.Contains(stdout.String(), tc.want) {
				t.Fatalf("%v: %v %s stderr=%s", tc.args, err, stdout.String(), stderr.String())
			}
			if len(tc.args) == 1 && tc.args[0] == "--fields" {
				var docs queryFieldDocs
				if err := json.Unmarshal(stdout.Bytes(), &docs); err != nil {
					t.Fatal(err)
				}
				seen := map[string]bool{}
				for _, field := range append(docs.IssueFields, docs.CrossEntityFields...) {
					seen[field.Name] = true
					parsed, err := query.Parse("has(" + field.Name + ")")
					if err != nil || len(parsed.Validate()) != 0 {
						t.Fatal("advertised field is not valid TDQ", field.Name, err)
					}
				}
				for _, name := range []string{"sprint", "dep.blocks", "note.content", "due_date", "defer_until"} {
					if !seen[name] {
						t.Fatal("missing supported field", name)
					}
				}
			}
			if len(tc.args) == 1 && tc.args[0] == "--examples" {
				var report struct {
					Examples []queryExampleDoc `json:"examples"`
				}
				if err := json.Unmarshal(stdout.Bytes(), &report); err != nil || len(report.Examples) == 0 {
					t.Fatal(err)
				}
				for _, example := range report.Examples {
					parsed, err := query.Parse(example.Query)
					if err != nil || len(parsed.Validate()) != 0 {
						t.Fatal("advertised example is invalid", example.Query, err)
					}
				}
			}
		}
	}
	if _, err := os.Stat(calls); !os.IsNotExist(err) {
		t.Fatal("offline helper invoked gh", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("offline helper opened SQLite", err)
	}
}

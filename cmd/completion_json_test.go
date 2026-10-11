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

	"github.com/marcus/td/internal/config"
)

func TestCompletionCLIJSONAndOfflineStores(t *testing.T) {
	dir := githubScheduleTestDir(t)
	calls := recordScheduleGHCalls(t)
	contextRoot := t.TempDir()
	t.Setenv("TD_GH_SYNC_HELPER", "1")
	t.Setenv("TD_GH_CONTEXT_ROOT", contextRoot)
	t.Setenv("TD_FEATURE_SYNC_AUTOSYNC", "true")
	t.Setenv("TD_ANALYTICS", "true")
	ghPath := filepath.Join(strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0], "gh")
	if err := os.WriteFile(ghPath, []byte("#!/bin/sh\nprintf 'unexpected gh call\\n' >> \"$TD_SYNC_STATUS_CALLS\"\nexit 9\n"), 0755); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	run := func(fail bool, args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		argv := append([]string{"-test.run=^TestGitHubSyncCLIHelperProcess$", "--", "--work-dir", dir, "--refresh", "completion"}, args...)
		child := exec.CommandContext(ctx, executable, argv...)
		child.Dir = dir
		var stdout, stderr bytes.Buffer
		child.Stdout, child.Stderr = &stdout, &stderr
		err := child.Run()
		if (err != nil) != fail {
			t.Fatalf("%v: %v stdout=%s stderr=%s", args, err, stdout.String(), stderr.String())
		}
		return stdout.Bytes()
	}
	for _, store := range []string{config.StoreGitHub, config.StoreSQLite} {
		if store == config.StoreSQLite {
			if err := config.SetStore(dir, store, nil); err != nil {
				t.Fatal(err)
			}
		}
		for _, enabled := range []string{"false", "true"} {
			t.Setenv("TD_FEATURE_SYNC_CLI", enabled)
			for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
				var described string
				for _, noDesc := range []bool{false, true} {
					args := []string{shell}
					if noDesc {
						args = append(args, "--no-descriptions")
					}
					raw := run(false, args...)
					var result struct {
						Shell  string `json:"shell"`
						Script string `json:"script"`
					}
					out := run(false, append(args, "--json")...)
					if err := json.Unmarshal(out, &result); err != nil || result.Shell != shell || result.Script != string(raw) || len(raw) == 0 {
						t.Fatalf("%s/%s/%s noDesc=%v: completion JSON does not preserve script: %v", store, enabled, shell, noDesc, err)
					}
					if noDesc && result.Script == described {
						t.Fatal("no-descriptions did not change script", shell)
					}
					described = result.Script
				}
			}
			for _, args := range [][]string{{"bash", "unused", "--json"}, {"unknown", "--json"}} {
				out := run(true, args...)
				if !json.Valid(out) || !strings.Contains(string(out), `"error":`) {
					t.Fatal("completion failure lacks JSON error", args)
				}
			}
			out := run(false, "bash", "--help", "--json")
			if !json.Valid(out) || !strings.Contains(string(out), `"command":"td completion bash"`) {
				t.Fatal("completion help lacks command JSON")
			}
		}
	}
	if data, _ := os.ReadFile(calls); len(data) != 0 {
		t.Fatal("completion invoked gh", string(data))
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("completion opened issue SQLite", err)
	}
	entries, err := os.ReadDir(contextRoot)
	if err != nil || len(entries) != 0 {
		t.Fatal("completion created issue session", entries, err)
	}
}

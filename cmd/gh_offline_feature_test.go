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

func TestGitHubOfflineFeatureJSONLifecycleAndSources(t *testing.T) {
	dir := githubScheduleTestDir(t)
	calls := recordScheduleGHCalls(t)
	contextRoot := t.TempDir()
	t.Setenv("TD_GH_SYNC_HELPER", "1")
	t.Setenv("TD_GH_CONTEXT_ROOT", contextRoot)
	t.Setenv("TD_FEATURE_SYNC_AUTOSYNC", "true")
	t.Setenv("TD_ANALYTICS", "true")
	for _, name := range []string{"TD_FEATURE_REVIEW_POLICY_MODE", "TD_FEATURE_BALANCED_REVIEW_POLICY", "TD_DISABLE_EXPERIMENTAL", "TD_DISABLE_FEATURE", "TD_DISABLE_FEATURES", "TD_ENABLE_FEATURE", "TD_ENABLE_FEATURES"} {
		t.Setenv(name, "")
	}
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
		argv := append([]string{"-test.run=^TestGitHubSyncCLIHelperProcess$", "--", "--work-dir", dir, "--json", "feature"}, args...)
		argv = append(argv, "--refresh")
		child := exec.CommandContext(ctx, executable, argv...)
		child.Dir = dir
		var stdout, stderr bytes.Buffer
		child.Stdout, child.Stderr = &stdout, &stderr
		err := child.Run()
		if (err != nil) != fail || !json.Valid(stdout.Bytes()) {
			t.Fatalf("%v: %v stdout=%s stderr=%s", args, err, stdout.String(), stderr.String())
		}
		if fail && !strings.Contains(stdout.String(), `"error":`) {
			t.Fatal("missing top-level error envelope", stdout.String())
		}
		return stdout.Bytes()
	}
	assert := func(data []byte, want map[string]any) {
		t.Helper()
		var result map[string]any
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatal(err)
		}
		for key, value := range want {
			if result[key] != value {
				t.Fatal(key, result, want)
			}
		}
	}
	for _, enabled := range []string{"false", "true"} {
		t.Setenv("TD_FEATURE_SYNC_CLI", enabled)
		assert(run(false, "get", "review_policy_mode"), map[string]any{"value": "trusted", "source": "default"})
		assert(run(false, "set", "review_policy_mode", "delegated"), map[string]any{"action": "set", "value": "delegated", "scope": "project"})
		assert(run(false, "get", "review_policy_mode"), map[string]any{"value": "delegated", "source": "config"})
		t.Setenv("TD_FEATURE_REVIEW_POLICY_MODE", "trusted")
		assert(run(false, "get", "review_policy_mode"), map[string]any{"value": "trusted", "source": "env"})
		var entries []featureListEntry
		if err := json.Unmarshal(run(false, "list"), &entries); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, entry := range entries {
			if entry.Name == "review_policy_mode" {
				found = true
				if entry.State != "trusted" || entry.Source != "env" || entry.Enabled != nil {
					t.Fatal(entry)
				}
			}
		}
		if !found {
			t.Fatal("missing policy inventory")
		}
		t.Setenv("TD_FEATURE_REVIEW_POLICY_MODE", "")
		t.Setenv("TD_FEATURE_BALANCED_REVIEW_POLICY", "false")
		run(true, "list") // Conflicting legacy and modern settings must be visible.
		run(true, "get", "review_policy_mode")
		t.Setenv("TD_FEATURE_BALANCED_REVIEW_POLICY", "")
		assert(run(false, "unset", "review_policy_mode"), map[string]any{"action": "unset", "scope": "project"})
		t.Setenv("TD_FEATURE_BALANCED_REVIEW_POLICY", "false")
		assert(run(false, "get", "review_policy_mode"), map[string]any{"value": "strict", "source": "env"})
		t.Setenv("TD_FEATURE_BALANCED_REVIEW_POLICY", "")
		assert(run(false, "set", "sync_autosync", "false"), map[string]any{"value": false, "action": "set"})
		// Set reports the stored override; get reports the higher-priority env.
		assert(run(false, "get", "sync_autosync"), map[string]any{"value": true, "source": "env"})
		assert(run(false, "unset", "sync_autosync"), map[string]any{"action": "unset"})
		before, err := os.ReadFile(filepath.Join(dir, ".todos", "config.json"))
		if err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"set", "review_policy_mode", "invalid"}, {"get", "missing"}, {"set", "sync_cli", "invalid"}, {"list", "unused"}} {
			run(true, args...)
		}
		after, err := os.ReadFile(filepath.Join(dir, ".todos", "config.json"))
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("failed feature operation changed configuration", err)
		}
	}
	cfg, err := config.Load(dir)
	if err != nil || cfg.Store != config.StoreGitHub || cfg.GitHub == nil || cfg.GitHub.Repo != "owner/repo" {
		t.Fatal("feature writes altered store selection", cfg, err)
	}
	if data, _ := os.ReadFile(calls); len(data) != 0 {
		t.Fatal("offline feature command called gh", string(data))
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("feature commands created SQLite", err)
	}
	entries, err := os.ReadDir(contextRoot)
	if err != nil || len(entries) != 0 {
		t.Fatal("feature commands created issue context", entries, err)
	}
}

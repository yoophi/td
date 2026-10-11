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
	"github.com/marcus/td/internal/syncconfig"
)

func TestOfflineConfigHelperProcess(t *testing.T) {
	if os.Getenv("TD_OFFLINE_CONFIG_HELPER") != "1" {
		return
	}
	file := os.Getenv("TD_OFFLINE_CONFIG_FILE")
	loadSyncCLIConfig = func() (*syncconfig.Config, error) {
		data, err := os.ReadFile(file)
		if os.IsNotExist(err) {
			return &syncconfig.Config{}, nil
		}
		if err != nil {
			return nil, err
		}
		var cfg syncconfig.Config
		err = json.Unmarshal(data, &cfg)
		return &cfg, err
	}
	saveSyncCLIConfig = func(cfg *syncconfig.Config) error {
		data, err := json.Marshal(cfg)
		if err != nil {
			return err
		}
		return os.WriteFile(file, data, 0600)
	}
	for i, arg := range os.Args {
		if arg == "--" {
			args := os.Args[i+1:]
			os.Args = append([]string{"td"}, args...)
			rootCmd.SetArgs(args)
			Execute()
			os.Exit(0)
		}
	}
	t.Fatal("missing helper arguments")
}

func TestGitHubOfflineConfigJSONContractsAndSQLiteWrites(t *testing.T) {
	dir := githubScheduleTestDir(t)
	calls := recordScheduleGHCalls(t)
	contextRoot := t.TempDir()
	file := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("TD_OFFLINE_CONFIG_HELPER", "1")
	t.Setenv("TD_OFFLINE_CONFIG_FILE", file)
	t.Setenv("TD_GH_CONTEXT_ROOT", contextRoot)
	t.Setenv("TD_ANALYTICS", "true")
	t.Setenv("TD_FEATURE_SYNC_AUTOSYNC", "true")
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
		argv := append([]string{"-test.run=^TestOfflineConfigHelperProcess$", "--", "--work-dir", dir, "--json", "config"}, args...)
		argv = append(argv, "--refresh")
		child := exec.CommandContext(ctx, executable, argv...)
		child.Dir = dir
		var stdout, stderr bytes.Buffer
		child.Stdout, child.Stderr = &stdout, &stderr
		err := child.Run()
		if (err != nil) != fail || !json.Valid(stdout.Bytes()) || (fail && !strings.Contains(stdout.String(), `"error":`)) {
			t.Fatalf("%v: %v stdout=%s stderr=%s", args, err, stdout.String(), stderr.String())
		}
		return stdout.Bytes()
	}
	for _, enabled := range []string{"false", "true"} {
		t.Setenv("TD_FEATURE_SYNC_CLI", enabled)
		run(false, "get", "store")
		run(false, "list")
		for _, args := range [][]string{{"get", "missing"}, {"set", "missing", "value"}, {"list", "unused"}, {"setup"}, {"set", "store", "sqlite", "--remote", "origin"}} {
			run(true, args...)
		}
		for _, key := range validConfigKeys {
			if !strings.HasPrefix(key, "sync.") {
				continue
			}
			run(true, "set", key, "true") // GH cannot change global SQLite settings.
			run(enabled == "false", "get", key)
		}
		if _, err := os.Stat(file); !os.IsNotExist(err) {
			t.Fatal("GH configuration operation wrote global fixture settings", err)
		}
	}
	// Exercise actual SQL config CLI parsing/persistence through only the temp
	// backend. No real global settings, HOME or issue database are changed.
	if err := config.SetStore(dir, config.StoreSQLite, nil); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TD_FEATURE_SYNC_CLI", "true")
	var receipt map[string]any
	if err := json.Unmarshal(run(false, "set", "sync.auto.enabled", "false"), &receipt); err != nil || receipt["scope"] != "global-sqlite" || receipt["value"] != "false" {
		t.Fatal(receipt, err)
	}
	var value string
	if err := json.Unmarshal(run(false, "get", "sync.auto.enabled"), &value); err != nil || value != "false" {
		t.Fatal(value, err)
	}
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	run(true, "set", "sync.auto.enabled", "invalid")
	after, err := os.ReadFile(file)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("invalid SQL config value changed persistence", err)
	}
	if data, _ := os.ReadFile(calls); len(data) != 0 {
		t.Fatal("offline config called gh", string(data))
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("configuration operation created SQLite", err)
	}
	entries, err := os.ReadDir(contextRoot)
	if err != nil || len(entries) != 0 {
		t.Fatal("configuration operation created issue context", entries, err)
	}
}

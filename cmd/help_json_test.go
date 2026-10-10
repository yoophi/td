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
)

func TestGitHubJSONHelpCLIContainsRealTreeAndFlagsOffline(t *testing.T) {
	dir := githubScheduleTestDir(t)
	calls := recordScheduleGHCalls(t)
	contextRoot := t.TempDir()
	t.Setenv("TD_GH_SYNC_HELPER", "1")
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
	for _, enabled := range []string{"false", "true"} {
		t.Setenv("TD_FEATURE_SYNC_CLI", enabled)
		for _, tc := range []struct {
			args []string
			path string
			flag string
		}{
			{[]string{"--help"}, "td", "json"},
			{[]string{"list", "--help"}, "td list", "filter"},
			{[]string{"ls", "--help"}, "td list", "limit"},
			{[]string{"task", "create", "--help"}, "td task create", "type"},
			{[]string{"doctor", "fk", "--help"}, "td doctor fk", "work-dir"},
			{[]string{"help", "list"}, "td list", "work-dir"},
		} {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			argv := append([]string{"-test.run=^TestGitHubSyncCLIHelperProcess$", "--", "--work-dir", dir, "--json"}, tc.args...)
			child := exec.CommandContext(ctx, executable, argv...)
			child.Dir = dir
			var stdout, stderr bytes.Buffer
			child.Stdout, child.Stderr = &stdout, &stderr
			err := child.Run()
			cancel()
			var result struct {
				helpJSONCommand
				Flags    []helpJSONFlag    `json:"flags"`
				Commands []helpJSONCommand `json:"commands"`
			}
			if err != nil || json.Unmarshal(stdout.Bytes(), &result) != nil || result.Command != tc.path {
				t.Fatalf("%v: %v stdout=%s stderr=%s", tc.args, err, stdout.String(), stderr.String())
			}
			found := false
			for _, flag := range result.Flags {
				if flag.Name == tc.flag {
					found = true
					if flag.Type == "" || flag.Usage == "" {
						t.Fatal("incomplete flag metadata", flag)
					}
					if tc.path != "td" && tc.flag == "work-dir" && !flag.Inherited {
						t.Fatal("missing inherited flag marker", flag)
					}
					if tc.path == "td task create" && tc.flag == "type" && !flag.Hidden {
						t.Fatal("hidden flag exposed without marker", flag)
					}
				}
			}
			if !found || result.Aliases == nil || result.Flags == nil || result.Commands == nil {
				t.Fatal("missing flag or null collections", result)
			}
			if tc.path == "td" && len(result.Commands) == 0 {
				t.Fatal("missing root command tree")
			}
			if tc.path == "td doctor fk" && !result.Hidden {
				t.Fatal("missing hidden command marker")
			}
		}
	}
	if data, _ := os.ReadFile(calls); len(data) != 0 {
		t.Fatal("help called gh", string(data))
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("help created SQLite", err)
	}
	entries, err := os.ReadDir(contextRoot)
	if err != nil || len(entries) != 0 {
		t.Fatal("help created issue session", entries, err)
	}
}

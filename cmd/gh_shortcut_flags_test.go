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

	"github.com/spf13/cobra"
)

func TestTypeShortcutRejectsConflictsBeforeExecution(t *testing.T) {
	for _, tc := range []struct {
		cmd    *cobra.Command
		create bool
		fixed  string
	}{{taskCreateCmd, true, "task"}, {epicCreateCmd, true, "epic"}, {taskListCmd, false, "task"}, {epicListCmd, false, "epic"}} {
		for _, value := range []string{"bug", "", tc.fixed + ",bug", "invalid"} {
			cmd := shortcutTestCommand(tc.cmd, tc.create)
			ran := false
			cmd.RunE = func(*cobra.Command, []string) error { ran = true; return nil }
			args := []string{"--type", value}
			if tc.create {
				args = append(args, "Shortcut fixture title")
			}
			cmd.SetArgs(args)
			if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "fixes --type") || ran {
				t.Fatal(tc.cmd.Name(), args, err, ran)
			}
		}
		cmd := shortcutTestCommand(tc.cmd, tc.create)
		ran := false
		cmd.RunE = func(*cobra.Command, []string) error { ran = true; return nil }
		args := []string{"--type", tc.fixed}
		if tc.create {
			args = append(args, "Shortcut fixture title")
		} else {
			args = append(args, "--type", tc.fixed+","+tc.fixed)
		}
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil || !ran {
			t.Fatal(args, err, ran)
		}
	}
}

func TestGitHubShortcutConflictingTypesCLIJSONBeforeGH(t *testing.T) {
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
		for _, args := range [][]string{
			{"task", "create", "Shortcut fixture title", "--type", "bug"},
			{"epic", "create", "Shortcut fixture title", "--type", "task"},
			{"task", "list", "--type", "bug"},
			{"epic", "list", "--type", "task"},
			{"task", "list", "--type", "task,bug"},
			{"task", "list", "--type", "task", "--type", "bug"},
		} {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			argv := append([]string{"-test.run=^TestGitHubSyncCLIHelperProcess$", "--", "--work-dir", dir, "--json"}, args...)
			child := exec.CommandContext(ctx, executable, argv...)
			child.Dir = dir
			var stdout, stderr bytes.Buffer
			child.Stdout, child.Stderr = &stdout, &stderr
			err := child.Run()
			cancel()
			if err == nil || !json.Valid(stdout.Bytes()) || !strings.Contains(stdout.String(), "fixes --type") {
				t.Fatalf("%v: %v stdout=%s stderr=%s", args, err, stdout.String(), stderr.String())
			}
		}
	}
	if data, _ := os.ReadFile(calls); len(data) != 0 {
		t.Fatal("invalid shortcut reached gh", string(data))
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("invalid shortcut created SQLite", err)
	}
}

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

// Run Execute in a separate process so feature registration, root hooks and
// the top-level error envelope are exercised together, including os.Exit.
func TestGitHubSyncCLIHelperProcess(t *testing.T) {
	if os.Getenv("TD_GH_SYNC_HELPER") != "1" {
		return
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

func TestGitHubSyncCLIEmptyResultsAndErrors(t *testing.T) {
	dir := githubScheduleTestDir(t)
	calls := recordScheduleGHCalls(t)
	state := []byte(`{"issues":{},"comments":[]}`)
	if err := os.WriteFile(os.Getenv("TD_SCHEDULE_STATE"), state, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TD_GH_SYNC_HELPER", "1")
	t.Setenv("TD_FEATURE_SYNC_AUTOSYNC", "true")
	t.Setenv("TD_AUTH_KEY", "fixture-only")
	t.Setenv("TD_SYNC_URL", "http://127.0.0.1:1")
	t.Setenv("TD_ANALYTICS", "false")
	t.Setenv("TD_GH_CONTEXT_ROOT", t.TempDir())
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
			{[]string{"sync"}, `"tasks":0`, false},
			{[]string{"sync", "--pull"}, `"remote_writes":0`, false},
			{[]string{"sync", "--status"}, `"mode":"direct-gh"`, false},
			{[]string{"sync", "init"}, `"action":"validate-existing-config"`, false},
			{[]string{"sync", "tail", "-n", "0"}, `"entries":[]`, false},
			{[]string{"sync", "conflicts"}, `"conflicts":[]`, false},
			{[]string{"list", "--all"}, `[]`, false},
			{[]string{"sync", "--push"}, `"error":`, true},
		} {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			args := append([]string{"-test.run=^TestGitHubSyncCLIHelperProcess$", "--", "--work-dir", dir, "--json"}, tc.args...)
			command := exec.CommandContext(ctx, executable, args...)
			var stdout, stderr bytes.Buffer
			command.Stdout, command.Stderr = &stdout, &stderr
			err := command.Run()
			cancel()
			if (err != nil) != tc.fail || ctx.Err() == context.DeadlineExceeded {
				t.Fatalf("feature=%s %v: %v stdout=%s stderr=%s", enabled, tc.args, err, stdout.String(), stderr.String())
			}
			if !json.Valid(stdout.Bytes()) || !strings.Contains(stdout.String(), tc.want) {
				t.Fatalf("feature=%s %v: invalid result %q stderr=%s", enabled, tc.args, stdout.String(), stderr.String())
			}
			if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
				t.Fatal("CLI opened SQLite", err)
			}
		}
	}
	after, err := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
	if err != nil || !bytes.Equal(state, after) {
		t.Fatal("CLI modified remote fixture", err)
	}
	invocations, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(invocations)), "\n") {
		var args []string
		if err := json.Unmarshal([]byte(line), &args); err != nil {
			t.Fatal(err)
		}
		for i, arg := range args {
			if arg == "--method" && (i+1 == len(args) || args[i+1] != "GET") {
				t.Fatal("read-only CLI attempted a write", args)
			}
		}
	}
}

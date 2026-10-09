package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestGitHubSyncParentsRefreshStatusAndNoPush(t *testing.T) {
	dir := githubScheduleTestDir(t)
	calls := recordScheduleGHCalls(t)
	for _, parent := range []*cobra.Command{syncCmd, syncAlwaysOnCmd} {
		for _, flag := range []string{"", "--pull", "--status"} {
			cmd := &cobra.Command{Use: "sync", RunE: parent.RunE, SilenceErrors: true, SilenceUsage: true}
			cmd.Flags().Bool("push", false, "")
			cmd.Flags().Bool("pull", false, "")
			cmd.Flags().Bool("status", false, "")
			cmd.Flags().Bool("json", false, "")
			var out bytes.Buffer
			cmd.SetOut(&out)
			args := []string{"--json"}
			if flag != "" {
				args = append(args, flag)
			}
			cmd.SetArgs(args)
			if err := cmd.Execute(); err != nil {
				t.Fatal(flag, err)
			}
			if flag == "--status" {
				var report githubSyncStatusReport
				if err := json.Unmarshal(out.Bytes(), &report); err != nil || report.Mode != "direct-gh" {
					t.Fatal(out.String(), err)
				}
			} else {
				var report githubSyncRefreshReport
				if err := json.Unmarshal(out.Bytes(), &report); err != nil || report.Action != "refresh" || report.Tasks != 4 || report.RemoteWrites != 0 || report.ObservationMode != "full" || report.ObservedAt.IsZero() {
					t.Fatal(flag, out.String(), err)
				}
			}
		}
	}
	before, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	issues, comments := 0, 0
	for _, line := range strings.Split(strings.TrimSpace(string(before)), "\n") {
		var args []string
		if err := json.Unmarshal([]byte(line), &args); err != nil {
			t.Fatal(err)
		}
		for i, arg := range args {
			if arg == "--method" && args[i+1] != "GET" {
				t.Fatal("sync attempted a remote write", args)
			}
			if strings.HasPrefix(arg, "repos/owner/repo/issues?state=all") {
				issues++
			}
			if strings.HasPrefix(arg, "repos/owner/repo/issues/comments?") {
				comments++
			}
		}
	}
	if issues != 4 || comments != 4 {
		t.Fatal("refresh did not use bulk fresh observations", issues, comments)
	}
	for _, args := range [][]string{{"--push"}, {"--push", "--pull"}, {"--status", "--pull"}} {
		cmd := &cobra.Command{Use: "sync", RunE: syncCmd.RunE, SilenceErrors: true, SilenceUsage: true}
		for _, name := range []string{"push", "pull", "status", "json"} {
			cmd.Flags().Bool(name, false, "")
		}
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err == nil || out.Len() != 0 {
			t.Fatal("unsupported operation reported success", args, err, out.String())
		}
	}
	after, err := os.ReadFile(calls)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("rejected options reached gh", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("sync opened SQLite", err)
	}
	if _, err := os.Stat(os.Getenv("TD_SCHEDULE_STATE")); !os.IsNotExist(err) {
		t.Fatal("sync changed GitHub state", err)
	}
}

func TestGitHubSyncCollectionFailureDoesNotEmitPartialSuccess(t *testing.T) {
	dir := githubScheduleTestDir(t)
	ghPath := filepath.Join(strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0], "gh")
	script, err := os.ReadFile(ghPath)
	if err != nil {
		t.Fatal(err)
	}
	old := "f=pathlib.Path(os.environ['TD_SCHEDULE_STATE'])"
	if !strings.Contains(string(script), old) {
		t.Fatal("fixture request parser changed")
	}
	script = []byte(strings.Replace(string(script), old, "if path=='repos/owner/repo/issues/comments?per_page=100':\n sys.stderr.write('HTTP 503 comment collection unavailable');sys.exit(1)\n"+old, 1))
	if err := os.WriteFile(ghPath, script, 0755); err != nil {
		t.Fatal(err)
	}
	cmd := &cobra.Command{Use: "sync", RunE: syncCmd.RunE, SilenceErrors: true, SilenceUsage: true}
	for _, name := range []string{"push", "pull", "status", "json"} {
		cmd.Flags().Bool(name, false, "")
	}
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--pull", "--json"})
	err = cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "HTTP 503 comment collection unavailable") || out.Len() != 0 {
		t.Fatal("partial collection looked successful or hid original error", err, out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("failed refresh opened SQLite", err)
	}
}

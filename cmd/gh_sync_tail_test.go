package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/models"
	"github.com/spf13/cobra"
)

type githubTailCancelWriter struct {
	out    *bytes.Buffer
	cancel context.CancelFunc
}

func (w githubTailCancelWriter) Write(data []byte) (int, error) {
	n, err := w.out.Write(data)
	w.cancel()
	return n, err
}

func TestGitHubSyncTailNativeHistoryAndFollowCancellation(t *testing.T) {
	githubScheduleTestDir(t)
	calls := recordScheduleGHCalls(t)
	stamp := time.Now().UTC().Format(time.RFC3339)
	state := map[string]any{"issues": map[string]any{"1": map[string]any{"number": 1, "title": "Observed task", "state": "open", "body": "", "labels": []any{}}}, "comments": []any{map[string]any{"id": 100, "issue_url": "https://api.github.com/repos/owner/repo/issues/1", "body": "Observed comment", "created_at": stamp, "updated_at": stamp, "user": map[string]any{"login": "native-author"}}}}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("TD_SCHEDULE_STATE"), data, 0600); err != nil {
		t.Fatal(err)
	}
	for _, follow := range []bool{false, true} {
		cmd := &cobra.Command{Use: syncTailCmd.Use, RunE: syncTailCmd.RunE, SilenceErrors: true, SilenceUsage: true}
		cmd.Flags().Bool("follow", follow, "")
		cmd.Flags().Int("lines", 20, "")
		cmd.Flags().Duration("interval", time.Minute, "")
		cmd.Flags().Bool("json", false, "")
		var out bytes.Buffer
		ctx, cancel := context.WithCancel(context.Background())
		cmd.SetContext(ctx)
		if follow {
			cmd.SetOut(githubTailCancelWriter{&out, cancel})
		} else {
			cmd.SetOut(&out)
		}
		cmd.SetArgs([]string{"--json"})
		err := cmd.Execute()
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		var report struct {
			Mode    string            `json:"mode"`
			Entries []models.Activity `json:"entries"`
		}
		if err := json.Unmarshal(out.Bytes(), &report); err != nil || report.Mode != "observed-github-activity" || len(report.Entries) != 1 || report.Entries[0].Message != "Observed comment" || report.Entries[0].Author != "native-author" {
			t.Fatal(out.String(), err)
		}
	}
	invocations, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(invocations), "issues?state=all") != 2 || strings.Count(string(invocations), "issues/comments?") != 2 {
		t.Fatal("tail refetched per issue or polled after cancellation", string(invocations))
	}
	after, err := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
	if err != nil || !bytes.Equal(data, after) {
		t.Fatal("tail changed remote state", err)
	}
}

func TestGitHubTailChangesSeedSkippedHistoryAndDeduplicateEdits(t *testing.T) {
	old := models.Activity{ID: "ghc-1", Kind: "comment", Message: "old"}
	seen := map[string][32]byte{}
	if rows := githubTailChanges([]models.Activity{old}, seen, true, 0); len(rows) != 0 {
		t.Fatal("-n 0 emitted history", rows)
	}
	if rows := githubTailChanges([]models.Activity{old}, seen, false, 20); len(rows) != 0 {
		t.Fatal("omitted history repeated", rows)
	}
	old.Message = "edited"
	if rows := githubTailChanges([]models.Activity{old}, seen, false, 20); len(rows) != 1 {
		t.Fatal("edit missed", rows)
	}
	if rows := githubTailChanges([]models.Activity{old}, seen, false, 20); len(rows) != 0 {
		t.Fatal("edit repeated", rows)
	}
}

func TestGitHubSyncTailRejectsInvalidPollingBeforeGH(t *testing.T) {
	githubScheduleTestDir(t)
	t.Setenv("PATH", t.TempDir())
	for _, options := range []struct {
		lines    int
		interval time.Duration
		message  string
	}{{-1, time.Minute, "lines must be nonnegative"}, {20, 29 * time.Second, "at least 30s"}} {
		cmd := &cobra.Command{Use: syncTailCmd.Use, Args: syncTailCmd.Args, RunE: syncTailCmd.RunE, SilenceErrors: true, SilenceUsage: true}
		cmd.Flags().Int("lines", options.lines, "")
		cmd.Flags().Duration("interval", options.interval, "")
		cmd.Flags().Bool("follow", false, "")
		var out bytes.Buffer
		cmd.SetOut(&out)
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), options.message) || out.Len() != 0 {
			t.Fatal("invalid options reached gh", err, out.String())
		}
	}
}

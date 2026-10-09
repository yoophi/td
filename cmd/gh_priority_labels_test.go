package cmd

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/marcus/td/internal/ghstore"
	"github.com/spf13/cobra"
)

func TestGitHubPriorityLabelSyncCLIPreviewApplyAndRepeat(t *testing.T) {
	githubScheduleTestDir(t)
	execute := func(args ...string) ghstore.PriorityLabelSyncResult {
		t.Helper()
		c := &cobra.Command{Use: syncPriorityLabelsCmd.Use, Args: syncPriorityLabelsCmd.Args, RunE: syncPriorityLabelsCmd.RunE, SilenceUsage: true, SilenceErrors: true}
		c.Flags().Bool("json", false, "")
		c.Flags().Bool("apply", false, "")
		c.Flags().StringSlice("id", nil, "")
		out, err := executeGitHubTest(c, append(args, "--json")...)
		var result ghstore.PriorityLabelSyncResult
		if err != nil || json.Unmarshal([]byte(out), &result) != nil {
			t.Fatalf("%s: %v", out, err)
		}
		return result
	}
	// Read-only previews must not even create the fake repository state file.
	preview := execute("--id", "gh-1")
	if preview.Apply || preview.Applied != 0 || len(preview.Repairs) != 1 {
		t.Fatalf("preview: %+v", preview)
	}
	if _, err := os.Stat(os.Getenv("TD_SCHEDULE_STATE")); !os.IsNotExist(err) {
		t.Fatal("preview wrote a state file", err)
	}
	execute("--id", "gh-1")
	if _, err := os.Stat(os.Getenv("TD_SCHEDULE_STATE")); !os.IsNotExist(err) {
		t.Fatal("repeat preview wrote a state file", err)
	}
	applied := execute("--id", "gh-1", "--apply")
	if !applied.Apply || applied.Applied != 1 {
		t.Fatalf("apply: %+v", applied)
	}
	before, err := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
	if err != nil {
		t.Fatal(err)
	}
	repeated := execute("--id", "gh-1", "--apply")
	if repeated.Applied != 0 || len(repeated.Repairs) != 0 {
		t.Fatalf("repeat: %+v", repeated)
	}
	after, err := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
	if err != nil || string(before) != string(after) {
		t.Fatal("no-op repeat changed remote fixture", err)
	}
}

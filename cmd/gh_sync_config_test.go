package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestGitHubSyncConfigRejectsGlobalWritesAndDeclaresScope(t *testing.T) {
	githubScheduleTestDir(t)
	for _, enabled := range []string{"true", "false"} {
		t.Setenv("TD_FEATURE_SYNC_CLI", enabled)
		for _, key := range validConfigKeys {
			if !strings.HasPrefix(key, "sync.") {
				continue
			}
			cmd := &cobra.Command{Use: configSetCmd.Use, Args: configSetCmd.Args, RunE: configSetCmd.RunE, SilenceErrors: true, SilenceUsage: true}
			cmd.Flags().String("remote", "origin", "")
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetArgs([]string{key, "true"})
			if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "global SQLite/td-sync setting") || out.Len() != 0 {
				t.Fatal("GitHub sync config mutation was accepted", key, err, out.String())
			}
		}
	}
	cmd := &cobra.Command{Use: configListCmd.Use, RunE: configListCmd.RunE}
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var values map[string]any
	if err := json.Unmarshal(out.Bytes(), &values); err != nil || values["sync_scope"] != "global-sqlite" || values["sync_applies_to_store"] != false || values["sync"] != nil {
		t.Fatal("scope unclear or disabled global settings loaded", out.String(), err)
	}
}

package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/ghstore"
	"github.com/spf13/cobra"
)

var syncPriorityLabelsCmd = &cobra.Command{
	Use:   "sync-priority-labels",
	Short: "Preview GitHub priority label repairs; --apply saves them",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		cmd.SetOut(cmd.OutOrStdout())
		cfg, err := config.Load(getBaseDir())
		if err != nil {
			return err
		}
		kind, err := config.Store(cfg)
		if err != nil {
			return err
		}
		if kind != config.StoreGitHub {
			return fmt.Errorf("sync-priority-labels requires store=gh-issue")
		}
		apply, _ := cmd.Flags().GetBool("apply")
		ids, _ := cmd.Flags().GetStringSlice("id")
		client, err := ghstore.Open(cmd.Context(), getBaseDir(), cfg.GitHub)
		if err != nil {
			return err
		}
		if apply {
			if err := trackGitHubCommandActions(cmd, client, cfg.GitHub.Repo); err != nil {
				return err
			}
		}
		result, err := client.SyncPriorityLabels(cmd.Context(), apply, ids...)
		if err != nil {
			return err
		}
		if jsonMode(cmd) {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
		}
		for _, repair := range result.Repairs {
			cmd.Printf("%s (%s): %v -> %v\n", repair.ID, repair.Priority, repair.Before, repair.After)
		}
		if apply {
			cmd.Printf("Applied %d priority label repairs\n", result.Applied)
		} else {
			cmd.Printf("Preview: %d repairs; no writes attempted. Use --apply to save a fresh plan.\n", len(result.Repairs))
		}
		return nil
	},
}

func init() {
	syncPriorityLabelsCmd.Flags().Bool("apply", false, "Apply repairs with revision conflict checks (default only previews)")
	syncPriorityLabelsCmd.Flags().StringSlice("id", nil, "Limit to selected td-managed issues (repeatable or comma-separated)")
	configCmd.AddCommand(syncPriorityLabelsCmd)
}

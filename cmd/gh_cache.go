package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/ghstore"
	"github.com/spf13/cobra"
)

func openGitHubCacheClient(cmd *cobra.Command) (*ghstore.Client, error) {
	cfg, err := config.Load(getBaseDir())
	if err != nil {
		return nil, err
	}
	store, err := config.Store(cfg)
	if err != nil {
		return nil, err
	}
	if store != config.StoreGitHub {
		return nil, fmt.Errorf("cache status/clear requires a configured gh-issue store; use --all to clear all GitHub snapshots")
	}
	return ghstore.Open(cmd.Context(), getBaseDir(), cfg.GitHub)
}

func init() {
	cache := &cobra.Command{Use: "cache", Short: "Inspect or clear regenerable GitHub snapshots", GroupID: "system"}
	cache.AddCommand(&cobra.Command{Use: "status", Args: cobra.NoArgs, Short: "Show GitHub snapshot paths, freshness and observation times", RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := openGitHubCacheClient(cmd)
		if err != nil {
			return err
		}
		status, err := client.CacheStatus(cmd.Context())
		if err != nil {
			return err
		}
		if jsonMode(cmd) {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(status)
		}
		cmd.Printf("GitHub snapshot cache: enabled=%t repository=%s TTL=%ds\n", status.Enabled, status.Repository, status.TTLSeconds)
		cmd.Printf("Full reconciliation interval: %ds\n", status.FullReconciliationSeconds)
		for _, entry := range status.Snapshots {
			cmd.Printf("%s: %s (%d bytes) %s\n", entry.Scope, entry.State, entry.Bytes, entry.Path)
			if entry.CollectedAt != nil {
				cmd.Printf("  collected_at: %s\n", entry.CollectedAt.Format("2006-01-02T15:04:05Z07:00"))
				if entry.FullReconciledAt != nil {
					cmd.Printf("  observation: %s; full_reconciled_at: %s\n", entry.ObservationMode, entry.FullReconciledAt.Format("2006-01-02T15:04:05Z07:00"))
				}
			}
		}
		return nil
	}})
	clear := &cobra.Command{Use: "clear", Args: cobra.NoArgs, Short: "Clear repository snapshots; --all clears all GitHub snapshots", RunE: func(cmd *cobra.Command, _ []string) error {
		all, _ := cmd.Flags().GetBool("all")
		var err error
		if all {
			err = ghstore.ClearAllSnapshotCaches(cmd.Context())
		} else {
			var client *ghstore.Client
			client, err = openGitHubCacheClient(cmd)
			if err == nil {
				err = client.ClearCache(cmd.Context())
			}
		}
		if err != nil {
			return err
		}
		if jsonMode(cmd) {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"cleared": true, "all": all})
		}
		cmd.Println("GitHub snapshots cleared; task, configuration and pending write state retained")
		return nil
	}}
	clear.Flags().Bool("all", false, "Clear all credential/repository/scope snapshots")
	cache.AddCommand(clear)
	rootCmd.AddCommand(cache)
}

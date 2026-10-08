package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/ghstore"
	"github.com/spf13/cobra"
)

var syncStateLabelsCmd = &cobra.Command{
	Use:   "sync-state-labels",
	Short: "Repair GitHub state labels from authoritative td metadata",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := config.Load(getBaseDir())
		if err != nil {
			return err
		}
		kind, err := config.Store(cfg)
		if err != nil {
			return err
		}
		if kind != config.StoreGitHub {
			return fmt.Errorf("sync-state-labels requires store=gh-issue")
		}
		client, err := ghstore.Open(cmd.Context(), getBaseDir(), cfg.GitHub)
		if err != nil {
			return err
		}
		count, err := client.SyncStateLabels(cmd.Context())
		if err != nil {
			return err
		}
		if jsonMode(cmd) {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]int{"repaired": count})
		}
		cmd.Printf("Repaired state labels on %d issues\n", count)
		return nil
	},
}

func init() { configCmd.AddCommand(syncStateLabelsCmd) }

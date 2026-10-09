package cmd

import (
	"fmt"
	"time"

	"github.com/marcus/td/internal/db"
	"github.com/marcus/td/internal/features"
	"github.com/marcus/td/internal/output"
	"github.com/spf13/cobra"
)

var syncConflictsCmd = &cobra.Command{
	Use:   "conflicts",
	Args:  cobra.NoArgs,
	Short: "Show recent sync conflicts",
	Long:  "Show recent sync conflicts. GitHub mode reads device-local typed CLI conflict diagnostics for the configured repository; it is not a repository-wide conflict registry.",
	RunE: func(cmd *cobra.Command, args []string) error {
		if handled, err := githubSyncConflicts(cmd); handled {
			return err
		}
		if !features.IsEnabledForProcess(features.SyncCLI.Name) {
			return fmt.Errorf("SQLite sync conflicts requires the sync_cli feature")
		}
		limit, _ := cmd.Flags().GetInt("limit")
		if limit <= 0 || limit > 1000 {
			output.Error("limit must be between 1 and 1000")
			return fmt.Errorf("invalid limit: %d", limit)
		}
		sinceStr, _ := cmd.Flags().GetString("since")

		baseDir := getBaseDir()
		database, err := db.Open(baseDir)
		if err != nil {
			output.Error("open database: %v", err)
			return err
		}
		defer func() { _ = database.Close() }()

		var since *time.Time
		if sinceStr != "" {
			d, err := time.ParseDuration(sinceStr)
			if err != nil {
				output.Error("invalid duration %q: %v", sinceStr, err)
				return err
			}
			t := time.Now().Add(-d)
			since = &t
		}

		conflicts, err := database.GetRecentConflicts(limit, since)
		if err != nil {
			output.Error("query conflicts: %v", err)
			return err
		}

		if len(conflicts) == 0 {
			fmt.Println("No sync conflicts found.")
			return nil
		}

		fmt.Println("Recent sync conflicts:")
		fmt.Printf("  %-21s %-9s %-10s %s\n", "TIME", "TYPE", "ENTITY", "SEQ")
		for _, c := range conflicts {
			fmt.Printf("  %-21s %-9s %-10s %d\n",
				c.OverwrittenAt.Format("2006-01-02 15:04:05"),
				c.EntityType,
				c.EntityID,
				c.ServerSeq,
			)
		}
		return nil
	},
}

func init() {
	syncConflictsCmd.Flags().Int("limit", 20, "Max conflicts to show")
	syncConflictsCmd.Flags().String("since", "", "Show conflicts from the last duration (e.g. 24h, 1h30m)")
}

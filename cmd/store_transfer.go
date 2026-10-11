package cmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/storetransfer"
	"github.com/spf13/cobra"
)

func newStoreTransferCommand() *cobra.Command {
	var destination, targetStore, sourceNamespace, targetNamespace, remote string
	var mappings []string
	var dryRun, execute bool
	var maxBytes int64
	cmd := &cobra.Command{
		Use:   "transfer",
		Short: "Transfer explicitly between SQLite and GitHub",
		Long:  "Transfer is separate from store selection. Choose --dry-run to inspect mappings and preservation limits without writes. --execute supports a SQLite destination; GitHub destination execution and resume are still in development.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if dryRun == execute {
				return fmt.Errorf("choose exactly one of --dry-run or --execute")
			}
			if execute && targetStore == config.StoreGitHub {
				return withErrorCode("unsupported_operation", fmt.Errorf("GitHub destination transfer execution is not yet available; no source or destination write attempted"))
			}
			if targetStore != config.StoreSQLite && targetStore != config.StoreGitHub {
				return fmt.Errorf("--to must explicitly be sqlite or gh-issue")
			}
			if destination == "" {
				return fmt.Errorf("--destination project directory is required")
			}
			if targetStore == config.StoreSQLite && (targetNamespace == "" || remote != "") {
				return fmt.Errorf("SQLite destination requires --destination-namespace and does not accept --remote")
			}
			if targetStore == config.StoreGitHub && remote == "" {
				return fmt.Errorf("GitHub destination requires an explicit --remote")
			}
			bindings := map[string]string{}
			for _, mapping := range mappings {
				key, id, found := strings.Cut(mapping, "=")
				if !found || key == "" || id == "" {
					return fmt.Errorf("--map requires a qualified-source-key=target-id")
				}
				if _, exists := bindings[key]; exists {
					return fmt.Errorf("duplicate --map source key %q", key)
				}
				bindings[key] = id
			}
			root := getBaseDir()
			cfg, err := config.Load(root)
			if err != nil {
				return err
			}
			sourceStore, err := config.Store(cfg)
			if err != nil {
				return err
			}
			if sourceStore == targetStore {
				return fmt.Errorf("store transfer requires different source and destination stores")
			}
			source, err := storetransfer.Collect(cmd.Context(), root, sourceNamespace, maxBytes)
			if err != nil {
				return fmt.Errorf("collect transfer source: %w", err)
			}
			target, err := storetransfer.InspectTarget(cmd.Context(), storetransfer.TargetSpec{ProjectDir: destination, Store: targetStore, Namespace: targetNamespace, Remote: remote}, maxBytes)
			if err != nil {
				return fmt.Errorf("inspect transfer destination: %w", err)
			}
			plan, err := storetransfer.BuildDataPlan(source.Source, target.Observation, bindings)
			if err != nil {
				return fmt.Errorf("plan transfer: %w", err)
			}
			if execute {
				result, err := storetransfer.ExecuteSQLiteDestination(cmd.Context(), source.Source, target, bindings, maxBytes)
				if err != nil {
					return err
				}
				for _, row := range plan.Disclosures {
					if row.Handling == "passive_archive" || row.Handling == "excluded" || row.Handling == "limitation" || row.Handling == "reuse_existing" {
						result.Preservation = append(result.Preservation, row)
					}
				}
				if jsonMode(cmd) {
					err = json.NewEncoder(cmd.OutOrStdout()).Encode(result)
				} else {
					_, err = fmt.Fprintf(cmd.OutOrStdout(), "Transfer committed to SQLite: tasks=%d notes=%d boards=%d positions=%d\nSource SHA256: %s\nDestination namespace: %s\n", result.Tasks, result.Notes, result.Boards, result.Positions, result.SourceSHA256, result.TargetNamespace)
					if err == nil {
						for _, row := range result.Preservation {
							_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s %s (%d): %s\n", row.Handling, row.Code, row.Count, row.Reason)
							if err != nil {
								break
							}
						}
					}
				}
				if err != nil {
					return fmt.Errorf("SQLite transfer committed (source_sha256=%s) but output failed; inspect destination archive before retry: %w", result.SourceSHA256, err)
				}
				return nil
			}
			if jsonMode(cmd) {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
					DryRun                    bool                    `json:"dry_run"`
					Writes                    int                     `json:"writes"`
					SourceStore               string                  `json:"source_store"`
					TargetStore               string                  `json:"target_store"`
					NeedsSQLiteInitialization bool                    `json:"needs_sqlite_initialization"`
					Plan                      *storetransfer.DataPlan `json:"plan"`
				}{true, 0, sourceStore, targetStore, target.NeedsSQLiteInitialization, plan})
			}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Transfer preview: %s -> %s\nWrites: 0\nIdentities: %d; relationships: %d; positions: %d\n", sourceStore, targetStore, len(plan.Identities.Assignments), len(plan.Relations), len(plan.Positions)); err != nil {
				return err
			}
			for _, row := range plan.Disclosures {
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s %s (%d): %s\n", row.Handling, row.Code, row.Count, row.Reason); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&targetStore, "to", "", "Destination store: sqlite or gh-issue")
	cmd.Flags().StringVar(&destination, "destination", "", "Existing destination project directory")
	cmd.Flags().StringVar(&sourceNamespace, "source-namespace", "", "Stable SQLite source namespace (GitHub uses its configured repository)")
	cmd.Flags().StringVar(&targetNamespace, "destination-namespace", "", "Stable SQLite destination namespace (GitHub uses its resolved repository)")
	cmd.Flags().StringVar(&remote, "remote", "", "Explicit GitHub destination git remote")
	cmd.Flags().StringArrayVar(&mappings, "map", nil, "Explicit qualified-source-key=existing-target-id binding; repeatable")
	cmd.Flags().Int64Var(&maxBytes, "max-bytes", 0, "Serialized input limit in bytes (0 uses 64 MiB)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Inspect a transfer without source or destination writes")
	cmd.Flags().BoolVar(&execute, "execute", false, "Execute to SQLite (GitHub destination execution is in development)")
	return cmd
}

func init() {
	storeCmd := &cobra.Command{Use: "store", Short: "Manage explicit store transfers", GroupID: "system", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() }}
	storeCmd.AddCommand(newStoreTransferCommand())
	rootCmd.AddCommand(storeCmd)
}

package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
	"github.com/spf13/cobra"
)

func importGitHubJournal(cmd *cobra.Command, base, file string, cfg *models.GitHubStoreConfig) error {
	if cmd.Flags().Changed("force") || cmd.Flags().Changed("source") {
		return fmt.Errorf("journal restore preserves source history without overwriting issues; omit --force and --source")
	}
	reader := cmd.InOrStdin()
	if file != "-" {
		input, err := os.Open(file)
		if err != nil {
			return fmt.Errorf("read journal input: %w", err)
		}
		defer func() { _ = input.Close() }()
		reader = input
	}
	data, err := io.ReadAll(io.LimitReader(reader, (64<<20)+1))
	if err != nil {
		return err
	}
	bundle, err := ghstore.ParsePortableJournal(data)
	if err != nil {
		return err
	}
	var mapping ghstore.JournalMapping
	path, _ := cmd.Flags().GetString("mapping")
	if cmd.Flags().Changed("mapping") && path == "" {
		return fmt.Errorf("--mapping requires a JSON file path")
	}
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read journal mapping: %w", err)
		}
		mapping, err = ghstore.ParseJournalMapping(data)
		if err != nil {
			return err
		}
	}
	if cfg == nil {
		return fmt.Errorf("gh-issue repository is not configured")
	}
	bundle, err = ghstore.BindJournal(bundle, cfg.Repo, mapping)
	if err != nil {
		return err
	}
	client, err := ghstore.Open(cmd.Context(), base, cfg)
	if err != nil {
		return err
	}
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	var plan *ghstore.JournalRestorePlan
	if dryRun {
		plan, err = client.PlanJournalRestore(cmd.Context(), bundle)
		if err != nil {
			return err
		}
		if jsonMode(cmd) {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(plan)
		}
		cmd.Printf("[dry-run] %d sources, %d actions, %d/%d archive parts present in %s\n", plan.Sources, plan.Actions, plan.PartsPresent, plan.Parts, plan.Repository)
	} else {
		if err := trackGitHubCommandActions(cmd, client, cfg.Repo); err != nil {
			return err
		}
		result, err := client.RestoreJournal(cmd.Context(), bundle)
		if err != nil {
			return err
		}
		if jsonMode(cmd) {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
		}
		plan = &result.Plan
		if result.AlreadyRestored {
			cmd.Printf("Journal archive already restored: %s (no write)\n", result.ArchiveID)
		} else {
			cmd.Printf("Journal archive restored: %s (%d new parts)\n", result.ArchiveID, result.WrittenParts)
		}
	}
	cmd.Printf("Mappings: %d; source-qualified unmapped references: %d\n", len(plan.Mappings), len(plan.Unmapped))
	for _, warning := range plan.Warnings {
		cmd.Printf("Note: %s\n", warning)
	}
	return nil
}

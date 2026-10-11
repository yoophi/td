package cmd

import (
	"encoding/json"
	"fmt"
	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/ghstore"
	"github.com/spf13/cobra"
	"io"
	"os"
	"strings"
)

func githubImport(cmd *cobra.Command, base, file string) (bool, error) {
	cfg, err := config.Load(base)
	if err != nil {
		return true, err
	}
	kind, err := config.Store(cfg)
	if err != nil {
		return true, err
	}
	format, _ := cmd.Flags().GetString("format")
	if kind != config.StoreGitHub {
		if cmd.Flags().Changed("source") {
			return true, fmt.Errorf("--source requires store=gh-issue")
		}
		if format == "journal" || cmd.Flags().Changed("mapping") {
			return true, fmt.Errorf("journal format and --mapping require store=gh-issue; no SQLite operation attempted")
		}
		return false, nil
	}
	if format == "journal" {
		return true, importGitHubJournal(cmd, base, file, cfg.GitHub)
	}
	if cmd.Flags().Changed("mapping") {
		return true, fmt.Errorf("--mapping requires --format journal")
	}
	if (format == "json" || format == "") && strings.HasSuffix(file, ".md") {
		format = "md"
	}
	if format != "json" && format != "md" {
		return true, fmt.Errorf("unsupported import format %q", format)
	}
	var data []byte
	if file == "-" {
		data, err = io.ReadAll(cmd.InOrStdin())
	} else {
		data, err = os.ReadFile(file)
	}
	if err != nil {
		return true, fmt.Errorf("read import input: %w", err)
	}
	var items []ghstore.ImportItem
	if format == "json" {
		items, err = ghstore.ParseImportJSON(data)
	} else {
		items, err = ghstore.ParseImportMarkdown(string(data))
	}
	if err != nil {
		return true, err
	}
	source, _ := cmd.Flags().GetString("source")
	if err := ghstore.SetImportSourceNamespace(items, source); err != nil {
		return true, err
	}
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	if !dryRun {
		if err := ghstore.RequireImportSourceNamespaces(items); err != nil {
			return true, err
		}
	}
	client, err := ghstore.Open(cmd.Context(), base, cfg.GitHub)
	if err != nil {
		return true, err
	}
	force, _ := cmd.Flags().GetBool("force")
	if !dryRun {
		if err := trackGitHubCommandActions(cmd, client, cfg.GitHub.Repo); err != nil {
			return true, err
		}
		result, err := client.Import(cmd.Context(), items, force)
		if err != nil {
			return true, err
		}
		if jsonMode(cmd) {
			return true, json.NewEncoder(cmd.OutOrStdout()).Encode(result)
		}
		for _, row := range result.Plan.Rows {
			cmd.Printf("%s %s -> %s\n", row.Action, row.SourceKey, row.TargetID)
		}
		for _, warning := range result.Plan.Warnings {
			cmd.Printf("Note: %s\n", warning)
		}
		cmd.Printf("\nImport complete: %d completed, %d skipped, %d created, %d applied, %d detached, %d archived parts in %s\n", result.Completed, result.Skipped, result.Created, result.Applied, result.Detached, result.ArchivedParts, result.Plan.Repository)
		return true, nil
	}
	plan, err := client.PlanImport(cmd.Context(), items, force)
	if err != nil {
		return true, err
	}
	plan.DryRun = dryRun
	if jsonMode(cmd) {
		return true, json.NewEncoder(cmd.OutOrStdout()).Encode(plan)
	}
	for _, r := range plan.Rows {
		target := r.TargetID
		if target == "" {
			target = "new GitHub issue number"
		}
		change := ""
		if r.SourceChanged {
			change = " (changed since export)"
		}
		cmd.Printf("[dry-run] %s %s -> %s%s\n", r.Action, r.SourceKey, target, change)
	}
	for _, warning := range plan.Warnings {
		cmd.Printf("Note: %s\n", warning)
	}
	cmd.Printf("\nPlanned %d issues in %s\n", len(plan.Rows), plan.Repository)
	return true, nil
}

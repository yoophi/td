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
	if kind != config.StoreGitHub {
		return false, nil
	}
	format, _ := cmd.Flags().GetString("format")
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
	client, err := ghstore.Open(cmd.Context(), base, cfg.GitHub)
	if err != nil {
		return true, err
	}
	force, _ := cmd.Flags().GetBool("force")
	plan, err := client.PlanImport(cmd.Context(), items, force)
	if err != nil {
		return true, err
	}
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	if !dryRun && len(items) > 0 {
		return true, fmt.Errorf("GitHub import writes are not yet available; validated %d items, no remote changes made; use --dry-run to inspect the plan", len(items))
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

package cmd

import (
	"encoding/json"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/ghstore"
	"github.com/spf13/cobra"
)

type githubSyncStatusReport struct {
	Action           string               `json:"action"`
	Store            string               `json:"store"`
	Repository       string               `json:"repository"`
	Mode             string               `json:"mode"`
	LocalQueue       bool                 `json:"local_queue"`
	TDSyncServerUsed bool                 `json:"td_sync_server_used"`
	Cache            *ghstore.CacheStatus `json:"cache"`
	Notes            []string             `json:"notes"`
}

// GitHub sync diagnostics validate the configured origin/authentication without
// opening SQLite, consulting td-sync credentials or fetching task collections.
func githubSyncStatus(cmd *cobra.Command) (bool, error) {
	return githubSyncStatusForAction(cmd, "status")
}

func githubSyncStatusForAction(cmd *cobra.Command, action string) (bool, error) {
	cfg, err := config.Load(getBaseDir())
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
	client, err := ghstore.Open(cmd.Context(), getBaseDir(), cfg.GitHub)
	if err != nil {
		return true, err
	}
	cache, err := client.CacheStatus(cmd.Context())
	if err != nil {
		return true, err
	}
	report := githubSyncStatusReport{Action: action, Store: "gh-issue", Repository: cfg.GitHub.Repo, Mode: "direct-gh", Cache: cache, Notes: []string{
		"GitHub reads and writes use gh directly; there is no local pending-event queue or td-sync server replay.",
		"Cache freshness describes reusable observations, not a completed push/pull or an atomic repository snapshot.",
		"Use td --refresh <read command> for a new observation; SQLite snapshot bootstrap and FK repair do not apply.",
	}}
	if action == "validate-existing-config" {
		report.Notes = append([]string{"Existing GitHub store configuration validated; no repository, td-sync project or configuration was created or changed."}, report.Notes...)
	}
	if jsonMode(cmd) {
		return true, json.NewEncoder(cmd.OutOrStdout()).Encode(report)
	}
	cmd.SetOut(cmd.OutOrStdout())
	cmd.Printf("GitHub sync: %s (%s)\n", report.Repository, report.Mode)
	cmd.Println("Local pending-event queue: none; td-sync server: unused")
	cmd.Printf("Snapshot cache: enabled=%t TTL=%ds full reconciliation=%ds\n", cache.Enabled, cache.TTLSeconds, cache.FullReconciliationSeconds)
	for _, snapshot := range cache.Snapshots {
		cmd.Printf("  %s: %s\n", snapshot.Scope, snapshot.State)
	}
	for _, note := range report.Notes {
		cmd.Printf("Note: %s\n", note)
	}
	return true, nil
}

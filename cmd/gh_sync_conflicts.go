package cmd

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/db"
	"github.com/marcus/td/internal/output"
	"github.com/spf13/cobra"
)

type githubConflictEntry struct {
	Timestamp  time.Time `json:"timestamp"`
	EntityID   string    `json:"entity_id"`
	AfterWrite bool      `json:"after_write"`
	Error      string    `json:"error"`
}

func githubSyncConflicts(cmd *cobra.Command) (bool, error) {
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
	if cfg.GitHub == nil || cfg.GitHub.Repo == "" {
		return true, fmt.Errorf("GitHub repository configuration is missing")
	}
	limit, _ := cmd.Flags().GetInt("limit")
	if limit < 1 || limit > 1000 {
		return true, fmt.Errorf("limit must be between 1 and 1000")
	}
	sinceText, _ := cmd.Flags().GetString("since")
	var since time.Time
	if sinceText != "" {
		duration, err := time.ParseDuration(sinceText)
		if err != nil || duration < 0 {
			return true, fmt.Errorf("invalid nonnegative duration %q", sinceText)
		}
		since = time.Now().Add(-duration)
	}
	observed, err := db.ReadAgentErrors(getBaseDir())
	if err != nil {
		return true, err
	}
	entries := []githubConflictEntry{}
	for i := len(observed) - 1; i >= 0; i-- {
		e := observed[i]
		if e.GitHubConflict == nil || !strings.EqualFold(e.GitHubConflict.Repository, cfg.GitHub.Repo) || e.Timestamp.Before(since) {
			continue
		}
		entries = append(entries, githubConflictEntry{Timestamp: e.Timestamp, EntityID: e.GitHubConflict.EntityID, AfterWrite: e.GitHubConflict.AfterWrite, Error: e.Error})
		if len(entries) == limit {
			break
		}
	}
	note := "Device-local recorded CLI conflicts only; absence of records does not prove the repository has no conflicts. Other devices, HTTP/TUI errors and legacy untyped diagnostics are not included. After-write conflicts may already have applied; inspect fresh remote state before retrying."
	if jsonMode(cmd) {
		return true, json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"store": "gh-issue", "repository": cfg.GitHub.Repo, "coverage": "device-local-cli", "conflicts": entries, "note": note})
	}
	cmd.SetOut(cmd.OutOrStdout())
	cmd.Printf("Recorded GitHub CLI conflicts for %s (%d)\n", cfg.GitHub.Repo, len(entries))
	for _, e := range entries {
		cmd.Printf("%s %s after_write=%t: %s\n", e.Timestamp.Format(time.RFC3339), output.SanitizeIssueText(e.EntityID), e.AfterWrite, output.SanitizeIssueText(e.Error))
	}
	cmd.Printf("Note: %s\n", note)
	return true, nil
}

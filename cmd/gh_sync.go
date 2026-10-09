package cmd

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/ghstore"
	"github.com/spf13/cobra"
)

type githubSyncRefreshReport struct {
	Store            string    `json:"store"`
	Repository       string    `json:"repository"`
	Action           string    `json:"action"`
	ObservedAt       time.Time `json:"observed_at"`
	FullReconciledAt time.Time `json:"full_reconciled_at"`
	ObservationMode  string    `json:"observation_mode"`
	Tasks            int       `json:"tasks"`
	Activities       int       `json:"activities"`
	RemoteWrites     int       `json:"remote_writes"`
	Notes            []string  `json:"notes"`
}

func githubSync(cmd *cobra.Command, args []string) (bool, error) {
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
	if len(args) != 0 {
		return true, fmt.Errorf("sync does not accept positional arguments")
	}
	push, _ := cmd.Flags().GetBool("push")
	pull, _ := cmd.Flags().GetBool("pull")
	status, _ := cmd.Flags().GetBool("status")
	if status && (push || pull) || push && pull {
		return true, fmt.Errorf("choose only one of --status, --push or --pull; no GitHub or SQLite changes attempted")
	}
	if push {
		return true, fmt.Errorf("gh-issue has no local pending-event queue to push; task commands write directly through gh; inspect remote state before retrying uncertain writes; no GitHub or SQLite changes attempted")
	}
	if status {
		handled, err := githubSyncStatus(cmd)
		if !handled {
			return true, fmt.Errorf("store configuration changed while reading sync status; run again")
		}
		return true, err
	}
	ctx := ghstore.WithFreshSnapshot(cmd.Context())
	client, err := ghstore.Open(ctx, getBaseDir(), cfg.GitHub)
	if err != nil {
		return true, err
	}
	snapshot, err := client.ReadSnapshot(ctx, true)
	if err != nil {
		return true, err
	}
	records := snapshot.Issues(true)
	report := githubSyncRefreshReport{Store: "gh-issue", Repository: cfg.GitHub.Repo, Action: "refresh", ObservedAt: snapshot.ObservedAt(), FullReconciledAt: snapshot.FullReconciledAt(), ObservationMode: snapshot.ObservationMode(), Tasks: len(records), Notes: []string{"Fresh GitHub observation; no remote writes or local event replay.", "Observations are best-effort, not an atomic repository snapshot; uncertain writes are never retried by sync."}}
	for _, record := range records {
		activity, err := snapshot.Activity(record.ID)
		if err != nil {
			return true, err
		}
		report.Activities += len(activity)
	}
	if jsonMode(cmd) {
		return true, json.NewEncoder(cmd.OutOrStdout()).Encode(report)
	}
	cmd.SetOut(cmd.OutOrStdout())
	cmd.Printf("Refreshed %s: %d tasks, %d native activities (%s observation)\n", report.Repository, report.Tasks, report.Activities, report.ObservationMode)
	for _, note := range report.Notes {
		cmd.Printf("Note: %s\n", note)
	}
	return true, nil
}

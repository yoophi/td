package cmd

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"syscall"
	"time"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/output"
	"github.com/spf13/cobra"
)

func githubSyncTail(cmd *cobra.Command) (bool, error) {
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
	lines, _ := cmd.Flags().GetInt("lines")
	if lines < 0 {
		return true, fmt.Errorf("lines must be nonnegative")
	}
	interval, _ := cmd.Flags().GetDuration("interval")
	if interval < 30*time.Second {
		return true, fmt.Errorf("GitHub tail interval must be at least 30s")
	}
	follow, _ := cmd.Flags().GetBool("follow")
	ctx := cmd.Context()
	if follow {
		var stop func()
		ctx, stop = signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()
	}
	client, err := ghstore.Open(ctx, getBaseDir(), cfg.GitHub)
	if err != nil {
		return true, err
	}
	cmd.SetOut(cmd.OutOrStdout())
	seen := map[string][32]byte{}
	initial := true
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		snapshot, err := client.ReadSnapshot(ctx, true)
		if err != nil {
			return true, err
		}
		entries, err := githubTailObservation(snapshot)
		if err != nil {
			return true, err
		}
		entries = githubTailChanges(entries, seen, initial, lines)
		if initial || len(entries) != 0 {
			if jsonMode(cmd) {
				err = json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"store": "gh-issue", "repository": cfg.GitHub.Repo, "observed_at": snapshot.ObservedAt(), "entries": entries, "mode": "observed-github-activity", "note": "Native issue activity and td transitions; not a push/pull queue or a lossless event stream."})
				if err != nil {
					return true, err
				}
			} else {
				if initial {
					cmd.Println("Observed GitHub issue activity (not a push/pull queue)")
				}
				for _, entry := range entries {
					cmd.Printf("%s %s [%s] %s\n", entry.CreatedAt.Format(time.RFC3339), entry.IssueID, entry.Kind, output.SanitizeIssueText(entry.Message))
					if entry.Kind == "handoff" {
						cmd.Printf("  done=%v remaining=%v decisions=%v uncertain=%v\n", sanitizeContextItems(entry.Done), sanitizeContextItems(entry.Remaining), sanitizeContextItems(entry.Decisions), sanitizeContextItems(entry.Uncertain))
					}
				}
				if initial && len(entries) == 0 {
					cmd.Println("No observed GitHub activity.")
				}
			}
		}
		if !follow {
			return true, nil
		}
		initial = false
		select {
		case <-ctx.Done():
			return true, nil
		case <-ticker.C:
		}
	}
}

func githubTailObservation(snapshot *ghstore.Snapshot) ([]models.Activity, error) {
	entries := []models.Activity{}
	for _, record := range snapshot.Issues(true) {
		activity, err := snapshot.Activity(record.ID)
		if err != nil {
			return nil, err
		}
		entries = append(entries, activity...)
		if record.Details != nil {
			for _, event := range record.Details.Transitions {
				entries = append(entries, models.Activity{ID: "ght-" + record.ID + "-" + event.OperationID, IssueID: record.ID, Kind: "transition", SessionID: event.SessionID, OperationID: event.OperationID, CreatedAt: event.At, UpdatedAt: event.At, Message: fmt.Sprintf("%s %s -> %s: %s", event.Action, event.From, event.To, event.Reason)})
			}
		}
	}
	slices.SortFunc(entries, func(a, b models.Activity) int {
		if order := a.CreatedAt.Compare(b.CreatedAt); order != 0 {
			return order
		}
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	return entries, nil
}

// Seed all initial identities, even omitted history, so -n 0 follows only new
// observations. Later changes retain their native identity and are emitted once.
func githubTailChanges(entries []models.Activity, seen map[string][32]byte, initial bool, limit int) []models.Activity {
	changed := []models.Activity{}
	for _, entry := range entries {
		data, _ := json.Marshal(entry)
		digest := sha256.Sum256(data)
		previous, exists := seen[entry.ID]
		seen[entry.ID] = digest
		if initial || !exists || previous != digest {
			changed = append(changed, entry)
		}
	}
	if initial && len(changed) > limit {
		changed = changed[len(changed)-limit:]
	}
	return changed
}

package cmd

import (
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/ghcontext"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/session"
	"github.com/spf13/cobra"
)

func init() {
	local := sessionCleanupCmd.RunE
	sessionCleanupCmd.RunE = func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(getBaseDir())
		if err != nil {
			return err
		}
		kind, err := config.Store(cfg)
		if err != nil {
			return err
		}
		if kind == config.StoreSQLite {
			return local(cmd, args)
		}
		return runGitHubSessionCleanup(cmd, args, cfg)
	}
}

// Cleanup only removes inactive identity history in the selected local scope.
// Other context files, current identity, bundles and all GitHub data are retained.
func runGitHubSessionCleanup(cmd *cobra.Command, args []string, cfg *models.Config) error {
	cmd.SetOut(cmd.OutOrStdout())
	if len(args) != 0 {
		return fmt.Errorf("session cleanup takes no arguments")
	}
	olderThan, _ := cmd.Flags().GetString("older-than")
	age, err := session.ParseDuration(olderThan)
	if err != nil {
		return err
	}
	if age <= 0 {
		return fmt.Errorf("--older-than must be positive")
	}
	force, _ := cmd.Flags().GetBool("force")
	client, err := ghstore.Open(cmd.Context(), getBaseDir(), cfg.GitHub)
	if err != nil {
		return err
	}
	dir, err := gitHubContextDirectory()
	if err != nil {
		return err
	}
	scope, err := ghcontext.Resolve(cmd.Context(), dir, cfg.GitHub.Repo)
	if err != nil {
		return err
	}
	state, err := scope.Update(cmd.Context(), nil)
	if err != nil {
		return err
	}
	records, err := client.List(cmd.Context(), true)
	if err != nil {
		return fmt.Errorf("check held GitHub claims before cleanup: %w", err)
	}
	held := githubCleanupHeldClaims(records)
	now := time.Now().UTC()
	candidates, kept := githubSessionCleanupPlan(state, held, now, age)
	if force && len(candidates) > 0 {
		_, err = scope.Update(cmd.Context(), func(current *ghcontext.State) error {
			if !reflect.DeepEqual(current, state) {
				return fmt.Errorf("local context changed since cleanup preview; no history removed")
			}
			// Re-read claims under the local lock. This is an observation, not a
			// distributed GitHub lock. No remote history or claims are modified.
			latest, err := client.List(cmd.Context(), true)
			if err != nil {
				return err
			}
			updated, updatedKept := githubSessionCleanupPlan(current, githubCleanupHeldClaims(latest), now, age)
			candidates, kept = updated, updatedKept
			ids := map[string]bool{}
			for _, sess := range candidates {
				ids[sess.ID] = true
			}
			current.History = slices.DeleteFunc(current.History, func(sess session.Session) bool { return ids[sess.ID] })
			return nil
		})
		if err != nil {
			return err
		}
	}
	action := "would_cleanup_sessions"
	if force {
		action = "cleaned_up_sessions"
	}
	if jsonMode(cmd) {
		row := func(sess session.Session) map[string]any {
			return map[string]any{"session": sess.ID, "branch": sess.Branch, "agent": sess.AgentType, "last_activity": sess.LastActive().UTC().Format(time.RFC3339), "age_seconds": int64(now.Sub(sess.LastActive()).Seconds())}
		}
		rows := make([]map[string]any, 0, len(candidates))
		for _, sess := range candidates {
			rows = append(rows, row(sess))
		}
		heldRows := make([]map[string]any, 0, len(kept))
		for _, holder := range kept {
			entry := row(holder.Session)
			entry["claims"] = holder.Claims
			entry["reason"] = "still holds unreleased GitHub claims; release them first"
			heldRows = append(heldRows, entry)
		}
		if len(kept) > 0 {
			action += "_with_held_claims"
		}
		return githubWSResult(cmd, action, map[string]any{"older_than": olderThan, "forced": force, "count": len(candidates), "sessions": rows, "held": heldRows, "held_count": len(kept), "scope": "current-device-local-context-history", "current_and_other_contexts_preserved": true, "work_bundles_preserved": true, "remote_history_preserved": true})
	}
	verb := "Would remove"
	if force {
		verb = "Removed"
	}
	cmd.Printf("%s %d local session history entries older than %s.\n", verb, len(candidates), olderThan)
	for _, sess := range candidates {
		cmd.Printf("  %s (%s)\n", sess.ID, sess.Branch)
	}
	for _, row := range kept {
		cmd.Printf("Kept %s: holds %d unreleased GitHub claim(s).\n", row.Session.ID, row.Claims)
	}
	cmd.Println("Current identity, other context files, work bundles and GitHub history are preserved.")
	if !force && len(candidates) > 0 {
		cmd.Println("Run with --force to remove these local history entries.")
	}
	return nil
}

type githubCleanupHolder struct {
	Session session.Session `json:"session"`
	Claims  int             `json:"claims"`
}

func githubCleanupHeldClaims(records []ghstore.Record) map[string]int {
	held := map[string]int{}
	for _, record := range records {
		if record.DeletedAt == nil && record.ImplementerSession != "" && (record.Status == models.StatusOpen || record.Status == models.StatusInProgress) {
			held[record.ImplementerSession]++
		}
	}
	return held
}
func githubSessionCleanupPlan(state *ghcontext.State, held map[string]int, now time.Time, age time.Duration) ([]session.Session, []githubCleanupHolder) {
	remove := []session.Session{}
	kept := []githubCleanupHolder{}
	for _, sess := range state.History {
		if sess.ID == state.Session.ID || now.Sub(sess.LastActive()) <= age {
			continue
		}
		if held[sess.ID] > 0 {
			kept = append(kept, githubCleanupHolder{Session: sess, Claims: held[sess.ID]})
			continue
		}
		// A corrupt historical duplicate must not remove an active bundle's actor.
		active, err := state.WorkSession(state.ActiveWorkSession)
		if err == nil && active.SessionID == sess.ID {
			continue
		}
		remove = append(remove, sess)
	}
	return remove, kept
}

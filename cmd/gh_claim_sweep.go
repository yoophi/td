package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/marcus/td/internal/ghcontext"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/session"
	"github.com/spf13/cobra"
)

type githubSweepClaim struct {
	ID           string    `json:"id"`
	Session      string    `json:"session"`
	IdleSeconds  float64   `json:"idle_seconds"`
	LastActivity time.Time `json:"last_activity"`
	Source       string    `json:"last_activity_source"`
	observed     ghstore.Record
}

func selectGitHubClaims(records []ghstore.Record, sessions []session.Session, self *session.Session, holder string, maxIdle time.Duration, now time.Time) ([]githubSweepClaim, []map[string]string) {
	known := map[string]session.Session{}
	for _, s := range sessions {
		if old, ok := known[s.ID]; !ok || s.LastActive().After(old.LastActive()) {
			known[s.ID] = s
		}
	}
	kin := sessionKin(sessions, self)
	claims := []githubSweepClaim{}
	unresolved := []map[string]string{}
	for _, record := range records {
		if record.Status != models.StatusInProgress && record.Status != models.StatusOpen {
			continue
		}
		owner := record.ImplementerSession
		if record.Status == models.StatusOpen && owner == "" {
			continue
		}
		if holder != "" && holder != owner {
			continue
		}
		if owner == "" {
			unresolved = append(unresolved, map[string]string{"id": record.ID, "session": owner, "reason": "no implementer session recorded"})
			continue
		}
		var activity time.Time
		if record.Details != nil {
			for _, h := range record.Details.Sessions {
				if h.CreatedAt.After(activity) {
					activity = h.CreatedAt
				}
			}
			for _, h := range record.Details.Transitions {
				if h.At.After(activity) {
					activity = h.At
				}
			}
		}
		local, ok := known[owner]
		last, source, measurable := claimLiveness(local, ok, &record.Issue, activity)
		idle := time.Duration(0)
		if measurable {
			idle = now.Sub(last)
		}
		if holder == "" {
			why := ""
			switch {
			case !measurable:
				why = "no usable GitHub or local activity timestamp"
			case idle <= maxIdle:
				continue
			case kin[owner]:
				why = "caller identity lineage is protected; release explicitly by ID or --session"
			}
			if why != "" {
				unresolved = append(unresolved, map[string]string{"id": record.ID, "session": owner, "reason": why})
				continue
			}
		}
		claims = append(claims, githubSweepClaim{ID: record.ID, Session: owner, IdleSeconds: idle.Seconds(), LastActivity: last, Source: source, observed: record})
	}
	slices.SortFunc(claims, func(a, b githubSweepClaim) int { return a.observed.Number - b.observed.Number })
	return claims, unresolved
}

func runGitHubClaimSweep(cmd *cobra.Command, client *ghstore.Client, scope ghcontext.Scope, state *ghcontext.State, options ghstore.TransitionOptions) error {
	holder, _ := cmd.Flags().GetString("session")
	stale, _ := cmd.Flags().GetString("stale")
	var maxIdle time.Duration
	var err error
	selector, value := "session", holder
	if stale != "" {
		selector, value = "stale", stale
		maxIdle, err = session.ParseDuration(stale)
		if err != nil || maxIdle <= 0 {
			return fmt.Errorf("--stale requires a positive duration: %q", stale)
		}
	}
	sessions, err := scope.List()
	if err != nil {
		return err
	}
	records, err := client.List(cmd.Context(), true)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, record := range records {
		if seen[record.ID] {
			return fmt.Errorf("claim pagination repeated %s; retry the read", record.ID)
		}
		seen[record.ID] = true
	}
	claims, unresolved := selectGitHubClaims(records, sessions, &state.Session, holder, maxIdle, time.Now())
	if holder != "" && len(claims) == 0 {
		known := false
		for _, s := range sessions {
			if s.ID == holder {
				known = true
			}
		}
		for _, record := range records {
			if record.ImplementerSession == holder || record.CreatorSession == holder {
				known = true
			}
			if record.Details != nil {
				for _, h := range record.Details.Sessions {
					if h.SessionID == holder {
						known = true
					}
				}
			}
		}
		if !known {
			return fmt.Errorf("session %q has no known local/shared identity or releasable claims", holder)
		}
	}
	reported := claims
	skipped := []map[string]string{}
	var failure error
	if options.Force {
		reported = []githubSweepClaim{}
		for _, claim := range claims {
			current, err := scope.Update(cmd.Context(), nil)
			if err != nil {
				failure = err
				break
			}
			if current.Session.ID != state.Session.ID {
				failure = fmt.Errorf("local session changed during claim sweep")
				break
			}
			if stale != "" {
				fresh, err := scope.List()
				if err != nil {
					failure = err
					break
				}
				eligible, _ := selectGitHubClaims([]ghstore.Record{claim.observed}, fresh, &current.Session, "", maxIdle, time.Now())
				if len(eligible) == 0 {
					skipped = append(skipped, map[string]string{"id": claim.ID, "reason": "local activity or identity lineage changed"})
					continue
				}
			}
			actor := options
			actor.Reason = fmt.Sprintf("claim released by --%s %s; previous holder %s", selector, value, claim.Session)
			if options.Reason != "" {
				actor.Reason += "; " + options.Reason
			}
			_, err = client.ReleaseObservedClaim(cmd.Context(), &claim.observed, actor)
			if err != nil {
				var conflict *ghstore.ConflictError
				if errors.As(err, &conflict) && !conflict.AfterWrite {
					skipped = append(skipped, map[string]string{"id": claim.ID, "reason": err.Error()})
					continue
				}
				failure = fmt.Errorf("release %s: %w", claim.ID, err)
				break
			}
			reported = append(reported, claim)
			_, err = scope.Update(cmd.Context(), func(local *ghcontext.State) error {
				if local.Session.ID != state.Session.ID {
					return fmt.Errorf("local session changed after release")
				}
				if local.Focus == claim.ID {
					local.Focus = ""
				}
				return nil
			})
			if err != nil {
				failure = fmt.Errorf("%s was released but local focus update failed: %w", claim.ID, err)
				break
			}
		}
	}
	verb := "preview"
	if options.Force {
		verb = "release"
	}
	payload := map[string]any{"action": verb + "_" + selector + "_claims", selector: value, "forced": options.Force, "count": len(reported), "claims": reported, "unresolved": unresolved, "unresolved_count": len(unresolved), "skipped": skipped, "liveness_scope": "GitHub issue/history plus device-local sessions; remote heartbeats unavailable"}
	if jsonMode(cmd) {
		if err := json.NewEncoder(cmd.OutOrStdout()).Encode(payload); err != nil {
			return err
		}
	} else {
		verb := "Would release"
		if options.Force {
			verb = "Released"
		}
		cmd.Printf("%s %d claims (--%s %s)\n", verb, len(reported), selector, value)
		for _, claim := range reported {
			cmd.Printf("  %s (session %s, idle %s, source %s)\n", claim.ID, claim.Session, formatIdle(time.Duration(claim.IdleSeconds*float64(time.Second))), claim.Source)
		}
		for _, row := range append(unresolved, skipped...) {
			cmd.Printf("  Preserved %s: %s\n", row["id"], row["reason"])
		}
		if !options.Force {
			cmd.Println("Run with --force to release.")
		}
		if stale != "" {
			cmd.Println("Remote session heartbeats are unavailable; stale detection is a heuristic.")
		}
	}
	if failure != nil {
		return fmt.Errorf("%d claims released before sweep stopped: %w", len(reported), failure)
	}
	return nil
}

func validateGitHubSweepFlags(cmd *cobra.Command) error {
	for _, flag := range []string{"session", "stale"} {
		value, _ := cmd.Flags().GetString(flag)
		if cmd.Flags().Changed(flag) && strings.TrimSpace(value) == "" {
			return fmt.Errorf("--%s requires a nonblank value", flag)
		}
	}
	stale, _ := cmd.Flags().GetString("stale")
	if stale != "" {
		d, err := session.ParseDuration(stale)
		if err != nil || d <= 0 {
			return fmt.Errorf("--stale requires a positive duration: %q", stale)
		}
	}
	return nil
}

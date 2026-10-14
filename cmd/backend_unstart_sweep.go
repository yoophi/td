package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/output"
	"github.com/marcus/td/internal/session"
	"github.com/spf13/cobra"
)

type backendSweepClaim struct {
	ID, Title, Holder, Revision, Source string
	IdleSeconds                         int64
	LastActivity                        time.Time
}
type backendSweepUnresolved struct {
	ID, Holder, Reason string
}
type backendSweepPlan struct {
	SelectorKey, SelectorValue string
	StaleNanos                 int64
	Claims                     []backendSweepClaim
	Unresolved                 []backendSweepUnresolved
}
type backendReleaseInput struct {
	Holder     string `json:"holder"`
	Mode       string `json:"mode"`
	StaleNanos int64  `json:"stale_nanos,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

func runBackendUnstartSweep(cmd *cobra.Command, holder, stale string) error {
	force, _ := cmd.Flags().GetBool("force")
	reason, _ := cmd.Flags().GetString("reason")
	selector, value, threshold := "session", holder, time.Duration(0)
	if stale != "" {
		selector, value = "stale", stale
		var err error
		threshold, err = session.ParseDuration(stale)
		if err != nil || threshold <= 0 {
			return fmt.Errorf("invalid --stale duration %q", stale)
		}
	}
	c, err := openBackendCommand(cmd)
	if err != nil {
		return err
	}
	if !force {
		plan, err := readBackendSweep(cmd.Context(), c, selector, value, threshold)
		if err != nil {
			return err
		}
		return emitBackendSweep(cmd, plan, false, nil, nil, nil)
	}
	var plan backendSweepPlan
	var released, skipped, failed []backendSweepClaim
	err = c.RunGroupIntent(cmd.Context(), backendInvocation(cmd, nil, "unstart-sweep"), func() (json.RawMessage, error) {
		var err error
		plan, err = readBackendSweep(cmd.Context(), c, selector, value, threshold)
		if err != nil {
			return nil, err
		}
		return json.Marshal(plan)
	}, func(groupID string, raw json.RawMessage) error {
		if err := json.Unmarshal(raw, &plan); err != nil {
			return fmt.Errorf("invalid stored backend claim sweep plan: %w", err)
		}
		if plan.SelectorKey != selector || plan.SelectorValue != value || plan.StaleNanos != int64(threshold) {
			return fmt.Errorf("stored backend claim sweep selector changed")
		}
		released, skipped, failed = nil, nil, nil
		for _, claim := range plan.Claims {
			var reply struct {
				Issue backendstore.Issue `json:"issue"`
			}
			input := backendReleaseInput{Holder: claim.Holder, Mode: selector, StaleNanos: plan.StaleNanos, Reason: reason}
			body, err := json.Marshal(input)
			if err != nil {
				return err
			}
			err = c.RunGroupStepIntent(cmd.Context(), struct{ Group, Issue string }{groupID, claim.ID}, func() (backendstore.MutationRequest, error) {
				return backendstore.MutationRequest{Method: "POST", Resource: "/issues/" + claim.ID + "/release", Revision: claim.Revision, Body: body}, nil
			}, &reply, func() error {
				if err := backendstore.ValidateIssue(reply.Issue); err != nil {
					return err
				}
				if reply.Issue.ID != claim.ID || reply.Issue.Status != models.StatusOpen || reply.Issue.ImplementerSession != "" {
					return fmt.Errorf("backend claim release response differs from request")
				}
				return nil
			})
			if err == nil {
				released = append(released, claim)
				continue
			}
			var api *backendstore.APIError
			if errors.As(err, &api) && (api.StatusCode == 404 || api.StatusCode == 409 || api.StatusCode == 412) {
				skipped = append(skipped, claim)
				plan.Unresolved = append(plan.Unresolved, backendSweepUnresolved{ID: claim.ID, Holder: claim.Holder, Reason: "claim, revision or liveness changed before release"})
				continue
			}
			if errors.As(err, &api) && api.StatusCode >= 400 && api.StatusCode < 500 {
				failed = append(failed, claim)
				plan.Unresolved = append(plan.Unresolved, backendSweepUnresolved{ID: claim.ID, Holder: claim.Holder, Reason: err.Error()})
				continue
			}
			return fmt.Errorf("released %d claim(s); stopped at %s (same command can recover pending receipt): %w", len(released), claim.ID, err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := emitBackendSweep(cmd, plan, true, released, skipped, failed); err != nil {
		return err
	}
	if len(released) == 0 && len(failed) > 0 {
		return errUnstartAllFailed
	}
	return nil
}

func readBackendSweep(ctx context.Context, c *backendstore.Connection, selector, value string, threshold time.Duration) (backendSweepPlan, error) {
	plan := backendSweepPlan{SelectorKey: selector, SelectorValue: value, StaleNanos: int64(threshold), Claims: []backendSweepClaim{}, Unresolved: []backendSweepUnresolved{}}
	err := c.ReadConsistent(ctx, func() error {
		plan.Claims, plan.Unresolved = []backendSweepClaim{}, []backendSweepUnresolved{}
		sessions, err := c.ListSessions(ctx)
		if err != nil {
			return err
		}
		rows, err := c.List(ctx, true)
		if err != nil {
			return err
		}
		byID := make(map[string]backendstore.Session, len(sessions))
		for _, row := range sessions {
			byID[row.ID] = row
		}
		kin := map[string]bool{c.SessionID: true}
		for changed := true; changed; {
			changed = false
			for _, row := range sessions {
				if row.PreviousSessionID == nil {
					continue
				}
				previous := *row.PreviousSessionID
				if kin[row.ID] && !kin[previous] {
					kin[previous], changed = true, true
				}
				if kin[previous] && !kin[row.ID] {
					kin[row.ID], changed = true, true
				}
			}
		}
		now := time.Now()
		for _, row := range rows {
			if row.Status != models.StatusInProgress && (row.Status != models.StatusOpen || row.ImplementerSession == "") {
				continue
			}
			holder := row.ImplementerSession
			if selector == "session" && holder != value {
				continue
			}
			if holder == "" {
				plan.Unresolved = append(plan.Unresolved, backendSweepUnresolved{ID: row.ID, Reason: "no implementer session recorded"})
				continue
			}
			last, source := row.UpdatedAt, "issue"
			if session, ok := byID[holder]; ok && session.LastActivity.After(last) {
				last, source = session.LastActivity, "session"
			}
			if selector == "stale" {
				activity, err := c.IssueActivityIncludingDeleted(ctx, row.ID)
				if err != nil {
					return err
				}
				for _, event := range activity {
					if event.CreatedAt.After(last) {
						last, source = event.CreatedAt, "issue_activity"
					}
				}
			}
			if last.IsZero() {
				plan.Unresolved = append(plan.Unresolved, backendSweepUnresolved{ID: row.ID, Holder: holder, Reason: "no usable activity timestamp"})
				continue
			}
			idle := now.Sub(last)
			if selector == "stale" {
				if idle <= threshold {
					continue
				}
				if kin[holder] {
					plan.Unresolved = append(plan.Unresolved, backendSweepUnresolved{ID: row.ID, Holder: holder, Reason: "held by caller's session lineage; use --session for an explicit release"})
					continue
				}
			}
			plan.Claims = append(plan.Claims, backendSweepClaim{ID: row.ID, Title: row.Title, Holder: holder, Revision: row.Revision, Source: source, IdleSeconds: int64(idle.Seconds()), LastActivity: last})
		}
		if selector == "session" && len(plan.Claims) == 0 {
			if _, ok := byID[value]; !ok {
				return fmt.Errorf("no session %s in this project, and no claim names it", value)
			}
		}
		slices.SortFunc(plan.Claims, func(a, b backendSweepClaim) int { return strings.Compare(a.ID, b.ID) })
		return nil
	})
	return plan, err
}

func emitBackendSweep(cmd *cobra.Command, plan backendSweepPlan, force bool, released, skipped, failed []backendSweepClaim) error {
	selected := plan.Claims
	verb := "would_release"
	if force {
		selected, verb = released, "released"
	}
	action := verb + "_" + plan.SelectorKey + "_claims"
	if len(plan.Unresolved) > 0 {
		action += "_with_unresolved"
	}
	if jsonMode(cmd) {
		claims := make([]map[string]any, 0, len(selected))
		for _, claim := range selected {
			claims = append(claims, map[string]any{"id": claim.ID, "title": claim.Title, "session": claim.Holder, "idle_seconds": claim.IdleSeconds, "last_activity": claim.LastActivity.UTC().Format(time.RFC3339), "last_activity_source": claim.Source})
		}
		unresolved := make([]map[string]any, 0, len(plan.Unresolved))
		for _, row := range plan.Unresolved {
			unresolved = append(unresolved, map[string]any{"id": row.ID, "session": row.Holder, "reason": row.Reason})
		}
		return output.EmitResult(action, map[string]any{plan.SelectorKey: plan.SelectorValue, "forced": force, "count": len(selected), "claims": claims, "unresolved": unresolved, "unresolved_count": len(unresolved), "skipped_count": len(skipped), "failed_count": len(failed)})
	}
	if !force {
		if len(plan.Claims) == 0 {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "No claims held by %s %s.\n", plan.SelectorKey, plan.SelectorValue)
		} else {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Will release %d claim(s) held by %s %s:\n", len(plan.Claims), plan.SelectorKey, plan.SelectorValue)
			for _, claim := range plan.Claims {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  - %s (session %s, idle %s)\n", claim.ID, claim.Holder, formatIdle(time.Duration(claim.IdleSeconds)*time.Second))
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "\nRun with --force to release.")
		}
	} else {
		for _, claim := range released {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "RELEASED %s → open (session %s)\n", claim.ID, claim.Holder)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Released %d, skipped %d, failed %d claim(s).\n", len(released), len(skipped), len(failed))
	}
	for _, row := range plan.Unresolved {
		output.Warning("not evaluated: %s (%s)", row.ID, row.Reason)
	}
	return nil
}

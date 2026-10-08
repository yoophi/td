package cmd

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/ghcontext"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func init() {
	for _, original := range []*cobra.Command{startCmd, unstartCmd, blockCmd, unblockCmd, reviewCmd, approveCmd, rejectCmd, closeCmd, reopenCmd} {
		local, command := original.RunE, original
		original.RunE = func(cmd *cobra.Command, args []string) error {
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
			cmd.SilenceUsage = true
			allowed := []string{"json", "work-dir", "help", "reason"}
			if command == startCmd {
				allowed = append(allowed, "force")
			}
			sweep := false
			if command == unstartCmd {
				allowed = append(allowed, "session", "stale", "force")
				if err := validateGitHubSweepFlags(cmd); err != nil {
					return err
				}
				holder, _ := cmd.Flags().GetString("session")
				stale, _ := cmd.Flags().GetString("stale")
				sweep = holder != "" || stale != ""
			}
			reviewAction := command == reviewCmd || command == approveCmd || command == rejectCmd || command == closeCmd || command == reopenCmd
			if reviewAction {
				allowed = append(allowed, "message", "comment", "note", "notes")
			}
			if command == reviewCmd {
				allowed = append(allowed, "minor")
			}
			if command == approveCmd {
				allowed = append(allowed, "all", "record-only", "self-review", "reviewed-by", "decision")
			}
			if command == closeCmd {
				allowed = append(allowed, "admin", "self-close-exception")
			}
			var unsupported error
			cmd.Flags().Visit(func(flag *pflag.Flag) {
				if !slices.Contains(allowed, flag.Name) {
					unsupported = fmt.Errorf("gh-issue does not yet support --%s for %s", flag.Name, command.Name())
				}
			})
			if unsupported != nil {
				return unsupported
			}
			options := ghstore.TransitionOptions{Reason: approvalReason(cmd)}
			options.Force, _ = cmd.Flags().GetBool("force")
			if reviewAction {
				options.Mode, err = resolveReviewPolicyMode(getBaseDir())
				if err != nil {
					return err
				}
				options.Minor, _ = cmd.Flags().GetBool("minor")
				options.RecordOnly, _ = cmd.Flags().GetBool("record-only")
				options.SelfReview, _ = cmd.Flags().GetBool("self-review")
				options.ReviewedBy, _ = cmd.Flags().GetString("reviewed-by")
				options.Decision, _ = cmd.Flags().GetString("decision")
				options.AdminReason, _ = cmd.Flags().GetString("admin")
				options.SelfCloseException, _ = cmd.Flags().GetString("self-close-exception")
				for _, flag := range []string{"reviewed-by", "admin", "self-close-exception"} {
					value, _ := cmd.Flags().GetString(flag)
					if cmd.Flags().Changed(flag) && strings.TrimSpace(value) == "" {
						return fmt.Errorf("--%s requires a nonblank value", flag)
					}
				}
				if err := ghstore.ValidateReviewOptions(command.Name(), options); err != nil {
					return err
				}
			}
			all, _ := cmd.Flags().GetBool("all")
			if all && len(args) > 0 {
				return fmt.Errorf("specify issue IDs or --all, not both")
			}
			if len(args) == 0 && command != approveCmd && !sweep {
				return fmt.Errorf("requires at least one issue ID")
			}
			for _, id := range args {
				if _, err := ghstore.Number(id); err != nil {
					return err
				}
			}
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
			options.SessionID = state.Session.ID
			if sweep {
				return runGitHubClaimSweep(cmd, client, scope, state, options)
			}
			if command == approveCmd && len(args) == 0 {
				candidates, err := client.ApprovalCandidates(cmd.Context(), options)
				if err != nil {
					return err
				}
				if len(candidates) == 0 {
					return fmt.Errorf("no issues eligible for approval by this session")
				}
				if !all && len(candidates) > 1 {
					ids := make([]string, len(candidates))
					for i, record := range candidates {
						ids[i] = record.ID
					}
					return fmt.Errorf("multiple issues await approval (%s); specify an issue ID or --all", strings.Join(ids, ", "))
				}
				for _, record := range candidates {
					args = append(args, record.ID)
				}
			}
			if command == startCmd {
				options.Snapshot, err = gitHubSnapshot(cmd.Context(), scope.Worktree)
				if err != nil {
					return fmt.Errorf("capture start snapshot: %w", err)
				}
			}
			completed := []string{}
			for _, id := range args {
				record, noop, err := client.Transition(cmd.Context(), id, command.Name(), options)
				if err != nil {
					if len(completed) > 0 {
						return fmt.Errorf("%s completed for %s; stopped at %s (earlier changes remain): %w", command.Name(), strings.Join(completed, ", "), id, err)
					}
					return err
				}
				_, err = scope.Update(cmd.Context(), func(current *ghcontext.State) error {
					if current.Session.ID != state.Session.ID {
						return fmt.Errorf("local session changed during transition")
					}
					if command == startCmd {
						current.Focus = record.ID
					}
					if (command == unstartCmd || command == reviewCmd || command == rejectCmd || record.Status == models.StatusClosed) && current.Focus == record.ID {
						current.Focus = ""
					}
					return nil
				})
				if err != nil {
					return fmt.Errorf("%s %s is saved on GitHub, but local focus update failed: %w", command.Name(), record.ID, err)
				}
				if jsonMode(cmd) {
					if err := json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"action": command.Name(), "id": record.ID, "status": record.Status, "issue": record, "noop": noop}); err != nil {
						return err
					}
				} else {
					cmd.Printf("%s %s [%s] (best-effort claim; no distributed lock)\n", command.Name(), record.ID, record.Status)
				}
				completed = append(completed, record.ID)
			}
			return nil
		}
	}
}

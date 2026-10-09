package cmd

import (
	"encoding/json"
	"fmt"
	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/ghcontext"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/output"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"strings"
)

// The resolver remains the same device-local context used by other writes.
// Keeping session acquisition separate lets fixture tests avoid user state.
var gitHubDeletionSession = func(cmd *cobra.Command, repo string) (string, error) {
	dir, err := gitHubContextDirectory()
	if err != nil {
		return "", err
	}
	scope, err := ghcontext.Resolve(cmd.Context(), dir, repo)
	if err != nil {
		return "", err
	}
	state, err := scope.Update(cmd.Context(), nil)
	if err != nil {
		return "", err
	}
	return state.Session.ID, nil
}

func init() {
	for _, original := range []*cobra.Command{deleteCmd, restoreCmd, deletedCmd} {
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
			allowed := " json work-dir help "
			if command != deletedCmd {
				allowed += "reason "
			}
			if command == deleteCmd {
				allowed += "force yes "
			}
			var invalid error
			cmd.Flags().Visit(func(f *pflag.Flag) {
				if !strings.Contains(allowed, " "+f.Name+" ") {
					invalid = fmt.Errorf("gh-issue does not support --%s for %s", f.Name, command.Name())
				}
			})
			if invalid != nil {
				return invalid
			}
			if command != deletedCmd && len(args) == 0 {
				return fmt.Errorf("requires at least one issue ID")
			}
			if command == deletedCmd && len(args) > 0 {
				return fmt.Errorf("deleted does not accept positional arguments")
			}
			reason, err := deletionReason(cmd)
			if err != nil {
				return err
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
			if command == deletedCmd {
				records, err := client.ListIncludingDeleted(cmd.Context(), true)
				if err != nil {
					return err
				}
				issues := []models.Issue{}
				for _, r := range records {
					if r.DeletedAt != nil {
						issues = append(issues, r.Issue)
					}
				}
				if jsonMode(cmd) {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(issues)
				}
				for _, issue := range issues {
					if _, err := fmt.Fprintln(cmd.OutOrStdout(), output.FormatIssueDeleted(&issue)); err != nil {
						return err
					}
				}
				if len(issues) == 0 {
					if _, err := fmt.Fprintln(cmd.OutOrStdout(), "No deleted issues"); err != nil {
						return err
					}
				}
				return nil
			}
			actor, err := gitHubDeletionSession(cmd, cfg.GitHub.Repo)
			if err != nil {
				return err
			}
			completed := []string{}
			results := []map[string]any{}
			for _, id := range args {
				observed, err := client.GetIncludingDeleted(cmd.Context(), id)
				var result *ghstore.Record
				var noop bool
				if err == nil {
					result, noop, err = client.SetDeletedObserved(cmd.Context(), observed, command == deleteCmd, actor, reason)
				}
				if err != nil {
					if len(completed) > 0 {
						return fmt.Errorf("%s completed for %s; stopped at %s (earlier changes remain): %w", command.Name(), strings.Join(completed, ", "), id, err)
					}
					return err
				}
				completed = append(completed, result.ID)
				results = append(results, map[string]any{"id": result.ID, "issue": result, "noop": noop})
				if !jsonMode(cmd) {
					if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", map[bool]string{true: "DELETED", false: "RESTORED"}[command == deleteCmd], result.ID); err != nil {
						return fmt.Errorf("%s saved for %s, but output failed; inspect current state before retrying: %w", command.Name(), strings.Join(completed, ", "), err)
					}
				}
			}
			if jsonMode(cmd) {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(results)
			}
			return nil
		}
	}
}

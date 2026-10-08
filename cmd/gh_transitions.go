package cmd

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/ghcontext"
	"github.com/marcus/td/internal/ghstore"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func init() {
	for _, original := range []*cobra.Command{startCmd, unstartCmd, blockCmd, unblockCmd} {
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
			var unsupported error
			cmd.Flags().Visit(func(flag *pflag.Flag) {
				if !slices.Contains(allowed, flag.Name) {
					unsupported = fmt.Errorf("gh-issue does not yet support --%s for %s", flag.Name, command.Name())
				}
			})
			if unsupported != nil {
				return unsupported
			}
			if len(args) == 0 {
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
			reason, _ := cmd.Flags().GetString("reason")
			force, _ := cmd.Flags().GetBool("force")
			options := ghstore.TransitionOptions{SessionID: state.Session.ID, Reason: reason, Force: force}
			if command == startCmd {
				options.Snapshot, err = gitHubSnapshot(cmd.Context(), scope.Worktree)
				if err != nil {
					return fmt.Errorf("capture start snapshot: %w", err)
				}
			}
			for _, id := range args {
				record, noop, err := client.Transition(cmd.Context(), id, command.Name(), options)
				if err != nil {
					return err
				}
				_, err = scope.Update(cmd.Context(), func(current *ghcontext.State) error {
					if current.Session.ID != state.Session.ID {
						return fmt.Errorf("local session changed during transition")
					}
					if command == startCmd {
						current.Focus = record.ID
					}
					if command == unstartCmd && current.Focus == record.ID {
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
			}
			return nil
		}
	}
}

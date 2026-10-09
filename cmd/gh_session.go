package cmd

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/ghcontext"
	"github.com/marcus/td/internal/issuestore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/output"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func init() {
	for _, command := range []*cobra.Command{sessionNameCmd, sessionListCmd, whoamiCmd, focusCmd, unfocusCmd, resumeCmd, usageCmd, statusCmd} {
		local := command.RunE
		original := command
		command.RunE = func(cmd *cobra.Command, args []string) error {
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
			return runGitHubSession(original, cmd, args, cfg)
		}
	}
}

func runGitHubSession(original, cmd *cobra.Command, args []string, cfg *models.Config) error {
	allowed := []string{"json", "work-dir", "help"}
	if original == sessionNameCmd {
		allowed = append(allowed, "new")
	}
	if original == usageCmd {
		allowed = append(allowed, "new-session", "compact", "quiet")
	}
	var unsupported error
	cmd.Flags().Visit(func(f *pflag.Flag) {
		if !slices.Contains(allowed, f.Name) {
			unsupported = fmt.Errorf("gh-issue does not support --%s for %s", f.Name, original.Name())
		}
	})
	if unsupported != nil {
		return unsupported
	}
	if original == statusCmd || original == usageCmd || original == whoamiCmd || original == resumeCmd {
		return runGitHubWorkContext(original, cmd, args, cfg)
	}
	reader, err := issuestore.OpenReader(cmd.Context(), getBaseDir())
	if err != nil {
		return err
	}
	defer func() { _ = reader.Close() }()
	dir, err := gitHubContextDirectory()
	if err != nil {
		return err
	}
	scope, err := ghcontext.Resolve(cmd.Context(), dir, cfg.GitHub.Repo)
	if err != nil {
		return err
	}
	var focused *issuestore.Record
	if original == focusCmd {
		if len(args) != 1 {
			return fmt.Errorf("requires one issue ID")
		}
		focused, err = reader.Get(cmd.Context(), args[0])
		if err != nil {
			return err
		}
	}
	previousFocus := ""
	state, err := scope.Update(cmd.Context(), func(state *ghcontext.State) error {
		previousFocus = state.Focus
		fresh, _ := cmd.Flags().GetBool("new")
		if fresh {
			scope.NewSession(state)
		}
		if original == sessionNameCmd && len(args) > 0 {
			state.Session.Name = args[0]
		}
		if focused != nil {
			state.Focus = focused.ID
		}
		if original == unfocusCmd {
			state.Focus = ""
		}
		return nil
	})
	if err != nil {
		return err
	}
	if original == sessionListCmd {
		sessions, err := scope.List()
		if err != nil {
			return err
		}
		if jsonMode(cmd) {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(sessions)
		}
		for _, sess := range sessions {
			cmd.Printf("%s %s [%s] (device-local)\n", sess.ID, output.SanitizeIssueText(sess.Name), output.SanitizeIssueText(sess.Branch))
		}
		return nil
	}
	if jsonMode(cmd) {
		payload := map[string]any{"action": original.Name(), "session": state.Session.ID, "name": state.Session.Name, "branch": state.Session.Branch, "agent": state.Session.AgentType, "started": state.Session.StartedAt, "previous_session": state.Session.PreviousSessionID, "storage": "device-local"}
		switch original {
		case focusCmd:
			payload["action"] = "focused"
			payload["id"] = focused.ID
			payload["status"] = focused.Status
			payload["issue"] = focused
		case unfocusCmd:
			payload["action"] = "unfocused"
			payload["previous_issue"] = previousFocus
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(payload)
	}
	cmd.Printf("SESSION: %s (device-local, branch %s)\n", output.SanitizeIssueText(state.Session.Display()), output.SanitizeIssueText(state.Session.Branch))
	if focused != nil {
		cmd.Printf("FOCUSED %s [%s] %s\n", focused.ID, focused.Status, output.SanitizeIssueText(focused.Title))
	}
	if original == unfocusCmd {
		cmd.Println("UNFOCUSED")
	}
	return nil
}

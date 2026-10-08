package cmd

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

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
	if original == focusCmd || original == resumeCmd {
		if len(args) != 1 {
			return fmt.Errorf("requires one issue ID")
		}
		focused, err = reader.Get(cmd.Context(), args[0])
		if err != nil {
			return err
		}
	}
	var issues []issuestore.Record
	if original == statusCmd || original == usageCmd {
		issues, err = reader.List(cmd.Context(), false)
		if err != nil {
			return err
		}
		slices.SortFunc(issues, func(a, b issuestore.Record) int { return strings.Compare(string(a.Priority), string(b.Priority)) })
	}
	previousFocus := ""
	state, err := scope.Update(cmd.Context(), func(state *ghcontext.State) error {
		previousFocus = state.Focus
		fresh, _ := cmd.Flags().GetBool("new")
		newContext, _ := cmd.Flags().GetBool("new-session")
		if fresh || newContext {
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
	if (original == statusCmd || original == usageCmd) && state.Focus != "" {
		focused, err = reader.Get(cmd.Context(), state.Focus)
		if err != nil {
			return fmt.Errorf("read focused issue %s: %w", state.Focus, err)
		}
	}
	if jsonMode(cmd) {
		payload := map[string]any{"action": original.Name(), "session": state.Session.ID, "name": state.Session.Name, "branch": state.Session.Branch, "agent": state.Session.AgentType, "started": state.Session.StartedAt, "previous_session": state.Session.PreviousSessionID, "storage": "device-local"}
		switch original {
		case focusCmd, resumeCmd:
			payload["action"] = "focused"
			payload["id"] = focused.ID
			payload["status"] = focused.Status
			payload["issue"] = focused
		case unfocusCmd:
			payload["action"] = "unfocused"
			payload["previous_issue"] = previousFocus
		case statusCmd, usageCmd:
			payload["focus"] = focused
			payload["issues"] = issues
			payload["review_workflows_supported"] = false
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(payload)
	}
	cmd.Printf("SESSION: %s (device-local, branch %s)\n", output.SanitizeIssueText(state.Session.Display()), output.SanitizeIssueText(state.Session.Branch))
	if focused != nil {
		cmd.Printf("FOCUSED %s [%s] %s\n", focused.ID, focused.Status, output.SanitizeIssueText(focused.Title))
	}
	if original == resumeCmd {
		cmd.Print(output.FormatIssueLong(output.SanitizedForDisplay(&focused.Issue), nil, nil))
	}
	if original == unfocusCmd {
		cmd.Println("UNFOCUSED")
	}
	if original == statusCmd || original == usageCmd {
		compact, _ := cmd.Flags().GetBool("compact")
		limit := len(issues)
		if compact && limit > 5 {
			limit = 5
		}
		for _, issue := range issues[:limit] {
			cmd.Printf("%s [%s] %s: %s\n", issue.Priority, issue.Status, issue.ID, output.SanitizeIssueText(issue.Title))
		}
		quiet, _ := cmd.Flags().GetBool("quiet")
		if !quiet {
			cmd.Println("GitHub issues are shared; session identity/focus are device-local. Use start/unstart/block/unblock for shared claims. Reviews and work sessions are not yet supported.")
		}
	}
	return nil
}

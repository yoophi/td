package cmd

import (
	"fmt"
	"slices"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/ghcontext"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/output"
	"github.com/spf13/cobra"
)

func init() {
	local := checkHandoffCmd.RunE
	checkHandoffCmd.RunE = func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
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
		return checkGitHubHandoff(cmd, args, cfg)
	}
}

func checkGitHubHandoff(cmd *cobra.Command, args []string, cfg *models.Config) error {
	cmd.SetOut(cmd.OutOrStdout())
	if len(args) != 0 {
		return fmt.Errorf("check-handoff takes no arguments")
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
	if state.ActiveWorkSession != "" {
		if _, err := state.CurrentWorkSession(); err != nil {
			return err
		}
	}
	records, err := client.List(cmd.Context(), true)
	if err != nil {
		return err
	}
	inProgress := []ghstore.Record{}
	for _, record := range records {
		if record.Status == models.StatusInProgress && record.ImplementerSession == state.Session.ID {
			inProgress = append(inProgress, record)
		}
	}
	slices.SortFunc(inProgress, func(a, b ghstore.Record) int { return a.Number - b.Number })
	ids := make([]string, 0, len(inProgress))
	for _, record := range inProgress {
		ids = append(ids, record.ID)
	}
	needed := len(ids) > 0 || state.ActiveWorkSession != ""
	if jsonMode(cmd) {
		if err := githubWSJSON(cmd, map[string]any{"needs_handoff": needed, "session": state.Session.ID, "in_progress_count": len(ids), "in_progress_issues": ids, "active_work_session": state.ActiveWorkSession, "focused_issue": state.Focus, "store": "gh-issue"}); err != nil {
			return err
		}
	} else if quiet, _ := cmd.Flags().GetBool("quiet"); !quiet {
		if needed {
			cmd.Println("HANDOFF NEEDED")
			for _, record := range inProgress {
				cmd.Printf("  %s  %s\n", record.ID, output.SanitizeIssueText(record.Title))
			}
			if len(ids) > 0 {
				cmd.Println("Run `td handoff <id>` before stopping work.")
			}
			if state.ActiveWorkSession != "" {
				cmd.Printf("Active work session: %s; run `td ws handoff`.\n", state.ActiveWorkSession)
			}
		} else {
			cmd.Println("No handoff needed - safe to exit")
		}
	}
	if needed {
		return errHandoffNeeded
	}
	return nil
}

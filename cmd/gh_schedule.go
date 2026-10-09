package cmd

import (
	"fmt"
	"time"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/dateparse"
	"github.com/marcus/td/internal/ghcontext"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
	"github.com/spf13/cobra"
)

func init() {
	for _, command := range []*cobra.Command{dueCmd, deferCmd} {
		localRun := command.RunE
		deferDate := command == deferCmd
		command.RunE = func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(getBaseDir())
			if err != nil {
				return err
			}
			store, err := config.Store(cfg)
			if err != nil {
				return err
			}
			if store == config.StoreSQLite {
				return localRun(cmd, args)
			}
			cmd.SilenceUsage = true
			cmd.SetOut(cmd.OutOrStdout())
			return runGitHubSchedule(cmd, args, cfg, deferDate)
		}
	}
}

func runGitHubSchedule(cmd *cobra.Command, args []string, cfg *models.Config, deferDate bool) error {
	if _, err := canonicalGitHubID(args[0]); err != nil {
		return err
	}
	clearDate, _ := cmd.Flags().GetBool("clear")
	value := ""
	if clearDate && len(args) > 1 {
		return fmt.Errorf("specify a date or --clear, not both")
	}
	if !clearDate {
		if len(args) < 2 {
			return fmt.Errorf("date argument required (or use --clear)")
		}
		var err error
		value, err = dateparse.ParseDate(args[1])
		if err != nil {
			return fmt.Errorf("invalid date: %w", err)
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
	change := ghstore.Changes{DueDate: &value}
	action, message := "due_date_set", "Due date set: "+value
	if clearDate {
		action, message = "due_date_cleared", "Due date cleared"
	}
	if deferDate {
		change = ghstore.Changes{DeferUntil: &value}
		action, message = "deferred", "Deferred until "+value
		if clearDate {
			action, message = "deferral_cleared", "Deferral cleared"
		}
	}
	record, err := client.Update(cmd.Context(), args[0], change)
	if err != nil {
		return err
	}
	if _, err := client.AppendActivity(cmd.Context(), record.ID, models.Activity{Kind: "log", LogType: models.LogTypeProgress, SessionID: state.Session.ID, WorkSessionID: state.ActiveWorkSession, Message: message}); err != nil {
		return fmt.Errorf("%s schedule was saved, but its log failed; do not repeat the entire schedule update: %w", record.ID, err)
	}
	return emitGitHubMutation(cmd, action, record)
}

type gitHubScheduleFilter struct{ all, deferred, overdue, surfacing, dueSoon bool }

func gitHubScheduleFilterFromFlags(cmd *cobra.Command, all bool) gitHubScheduleFilter {
	f := gitHubScheduleFilter{all: all}
	f.deferred, _ = cmd.Flags().GetBool("deferred")
	f.overdue, _ = cmd.Flags().GetBool("overdue")
	f.surfacing, _ = cmd.Flags().GetBool("surfacing")
	f.dueSoon, _ = cmd.Flags().GetBool("due-soon")
	return f
}

// Match SQLite's date('now','localtime'), including its flag precedence. All
// comparisons are calendar dates; UTC instants must not shift a local boundary.
func (f gitHubScheduleFilter) matches(issue models.Issue, now time.Time) bool {
	today := now.Format("2006-01-02")
	switch {
	case f.deferred:
		return issue.DeferUntil != nil && *issue.DeferUntil > today
	case f.overdue:
		return issue.DueDate != nil && *issue.DueDate < today && issue.Status != models.StatusClosed
	case f.surfacing:
		return issue.DeferUntil != nil && *issue.DeferUntil <= today && issue.DeferCount > 0
	case f.dueSoon:
		return issue.DueDate != nil && *issue.DueDate >= today && *issue.DueDate <= now.AddDate(0, 0, 3).Format("2006-01-02")
	case !f.all:
		return issue.DeferUntil == nil || *issue.DeferUntil <= today
	default:
		return true
	}
}

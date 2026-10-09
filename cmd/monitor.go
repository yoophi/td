package cmd

import (
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/db"
	"github.com/marcus/td/internal/output"
	"github.com/marcus/td/internal/session"
	"github.com/marcus/td/pkg/monitor"
	"github.com/spf13/cobra"
)

var monitorCmd = &cobra.Command{
	Use:   "monitor",
	Short: "Live TUI dashboard for observing agent activity",
	Long: `Launch a live-updating TUI dashboard showing:
- Current work: focused issue and in-progress tasks
- Activity log: recent logs, actions, and comments from all sessions
- Task list: ready, reviewable, and blocked issues

Key bindings:
  Tab/Shift+Tab  Switch panels
  ↑/↓            Select row in active panel
  j/k            Scroll viewport
  Enter          Open issue details modal
  Esc            Close modal
  r              Review selected issue / refresh activity panel
  ?              Toggle help
  q              Quit

Mouse support:
  Click          Select panel/row
  Double-click   Open issue details
  Scroll wheel   Scroll hovered panel`,
	GroupID: "system",
	RunE: func(cmd *cobra.Command, args []string) error {
		baseDir := getBaseDir()
		interval, _ := cmd.Flags().GetDuration("interval")
		if interval < 500*time.Millisecond {
			interval = 2 * time.Second
		}
		cfg, err := config.Load(baseDir)
		if err != nil {
			return err
		}
		store, err := config.Store(cfg)
		if err != nil {
			return err
		}
		if store == config.StoreGitHub {
			worktree, err := gitHubContextDirectory()
			if err != nil {
				return err
			}
			model, err := monitor.NewGitHubModelForWorktree(cmd.Context(), baseDir, worktree, interval, versionStr)
			if err != nil {
				return err
			}
			defer func() { _ = model.Close() }()
			final, runErr := tea.NewProgram(model).Run()
			if current, ok := final.(monitor.Model); ok && current.PendingRemoteWrite() {
				cmd.PrintErrln("GitHub write was pending when monitor exited; cancellation cannot undo an accepted write. Inspect GitHub before retrying.")
			}
			if err := runErr; err != nil {
				return fmt.Errorf("error running GitHub monitor: %w", err)
			}
			return nil
		}
		database, err := db.Open(baseDir)
		if err != nil {
			output.Error("%v", err)
			return err
		}
		sess, err := session.GetOrCreate(database)
		if err != nil {
			_ = database.Close()
			output.Error("%v", err)
			return err
		}

		model := monitor.NewModel(database, sess.ID, interval, versionStr, baseDir)
		defer func() { _ = model.Close() }()

		p := tea.NewProgram(model)
		if _, err := p.Run(); err != nil {
			return fmt.Errorf("error running monitor: %w", err)
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(monitorCmd)
	monitorCmd.Flags().Duration("interval", 2*time.Second, "Refresh interval (default 2s)")
}

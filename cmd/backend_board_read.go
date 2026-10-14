package cmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/models"
	"github.com/spf13/cobra"
)

func init() {
	for _, command := range []*cobra.Command{boardListCmd, boardShowCmd} {
		local, target := command.RunE, command
		command.RunE = func(cmd *cobra.Command, args []string) error {
			cfg, e := config.Load(getBaseDir())
			if e != nil {
				return e
			}
			kind, e := config.Store(cfg)
			if e != nil {
				return e
			}
			if kind == config.StoreBackend {
				return runBackendBoardRead(target, cmd, args)
			}
			return local(cmd, args)
		}
	}
}

func runBackendBoardRead(target, cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	c, e := openBackendCommand(cmd)
	if e != nil {
		return e
	}
	boards, e := c.Boards(cmd.Context())
	if e != nil {
		return e
	}
	if target == boardListCmd {
		if jsonMode(cmd) {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(boards)
		}
		if len(boards) == 0 {
			_, e = fmt.Fprintln(cmd.OutOrStdout(), "No boards found")
			return e
		}
		for _, b := range boards {
			builtin, query := "", ""
			if b.IsBuiltin {
				builtin = " [builtin]"
			}
			if b.Query != "" {
				query = " (" + b.Query + ")"
			}
			if _, e = fmt.Fprintf(cmd.OutOrStdout(), "%s: %s%s%s\n", b.ID, b.Name, query, builtin); e != nil {
				return e
			}
		}
		return nil
	}
	board, e := backendstore.ResolveBoard(boards, args[0])
	if e != nil {
		return e
	}
	requested, _ := cmd.Flags().GetStringArray("status")
	statuses := map[models.Status]bool{}
	for _, flag := range requested {
		for _, part := range strings.Split(flag, ",") {
			if part = strings.TrimSpace(part); part != "" {
				status := models.NormalizeStatus(part)
				if !models.IsValidStatus(status) {
					return fmt.Errorf("invalid board status %q", part)
				}
				statuses[status] = true
			}
		}
	}
	if len(statuses) == 0 {
		for _, status := range []models.Status{models.StatusOpen, models.StatusInProgress, models.StatusBlocked, models.StatusInReview} {
			statuses[status] = true
		}
	}
	detail, e := c.BoardDetail(cmd.Context(), board.ID, statuses[models.StatusClosed])
	if e != nil {
		return e
	}
	views := []models.BoardIssueView{}
	for _, card := range detail.Issues {
		if statuses[card.Issue.Status] {
			views = append(views, card.Model())
		}
	}
	if jsonMode(cmd) {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(views)
	}
	if _, e = fmt.Fprintf(cmd.OutOrStdout(), "Board: %s (%s)\n", board.Name, board.ID); e != nil {
		return e
	}
	if board.Query != "" {
		if _, e = fmt.Fprintf(cmd.OutOrStdout(), "Query: %s\n", board.Query); e != nil {
			return e
		}
	}
	if _, e = fmt.Fprintln(cmd.OutOrStdout()); e != nil {
		return e
	}
	if len(views) == 0 {
		_, e = fmt.Fprintln(cmd.OutOrStdout(), "No issues on this board")
		return e
	}
	for i, view := range views {
		position := fmt.Sprintf("(%d) ", i+1)
		if view.HasPosition {
			position = fmt.Sprintf("[%d] ", view.Position)
		}
		if _, e = fmt.Fprintf(cmd.OutOrStdout(), "%s%s %s %s [%s] %s\n", position, view.Issue.ID, getStatusIcon(view.Issue.Status), view.Issue.Priority, view.Issue.Type, view.Issue.Title); e != nil {
			return e
		}
	}
	return nil
}

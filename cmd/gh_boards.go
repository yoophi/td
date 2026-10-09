package cmd

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/issuestore"
	"github.com/marcus/td/internal/models"
	"github.com/spf13/cobra"
)

func init() {
	for _, original := range []*cobra.Command{boardListCmd, boardCreateCmd, boardDeleteCmd, boardShowCmd, boardEditCmd, boardMoveCmd, boardUnpositionCmd} {
		command, localRun := original, original.RunE
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
			return runGitHubBoard(cmd, args, cfg)
		}
	}
}

// Validate drafts before authentication or any carrier writes.
func gitHubBoardChanges(cmd *cobra.Command) (ghstore.BoardChanges, *string, error) {
	changes := ghstore.BoardChanges{}
	for _, field := range []string{"name", "query"} {
		if cmd.Flags().Changed(field) {
			value, _ := cmd.Flags().GetString(field)
			if field == "name" {
				changes.Name = &value
			} else {
				changes.Query = &value
			}
		}
	}
	if changes.Query != nil && *changes.Query != "" {
		if err := validateGitHubQuery(*changes.Query); err != nil {
			return changes, nil, err
		}
	}
	var mode *string
	if cmd.Flags().Changed("view-mode") {
		value, _ := cmd.Flags().GetString("view-mode")
		if value != "swimlanes" && value != "backlog" {
			return changes, nil, fmt.Errorf("board view mode must be swimlanes or backlog")
		}
		mode = &value
	}
	return changes, mode, nil
}

func runGitHubBoard(cmd *cobra.Command, args []string, cfg *models.Config) error {
	name := cmd.Name()
	if name == "list" && len(args) != 0 {
		return fmt.Errorf("board list does not accept positional arguments")
	}
	changes, mode, err := gitHubBoardChanges(cmd)
	if err != nil {
		return err
	}
	if name == "edit" && changes.Name == nil && changes.Query == nil && mode == nil {
		return fmt.Errorf("board edit requires --name, --query or --view-mode")
	}
	position := 0
	if name == "move" {
		position, err = strconv.Atoi(args[2])
		if err != nil || position < 1 {
			return fmt.Errorf("invalid position %q: must be a positive integer", args[2])
		}
	}
	statuses := map[models.Status]bool{}
	if name == "show" {
		values, _ := cmd.Flags().GetStringArray("status")
		for _, value := range mergeMultiValueFlag(values) {
			status := models.NormalizeStatus(value)
			if !models.IsValidStatus(status) {
				return fmt.Errorf("invalid board status: %s", value)
			}
			statuses[status] = true
		}
		if len(statuses) == 0 {
			for _, status := range []models.Status{models.StatusOpen, models.StatusInProgress, models.StatusBlocked, models.StatusInReview} {
				statuses[status] = true
			}
		}
	}
	client, err := ghstore.Open(cmd.Context(), getBaseDir(), cfg.GitHub)
	if err != nil {
		return err
	}
	state, err := resolveGitHubListState(cmd, cfg)
	if err != nil {
		return err
	}
	actor := state.Session.ID
	if name == "list" {
		boards, err := client.ListBoards(cmd.Context())
		if err != nil {
			return err
		}
		result := make([]models.Board, 0, len(boards))
		for _, board := range boards {
			result = append(result, board.Board)
		}
		if jsonMode(cmd) {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
		}
		for _, board := range result {
			builtin, query := "", ""
			if board.IsBuiltin {
				builtin = " [builtin]"
			}
			if board.Query != "" {
				query = " (" + board.Query + ")"
			}
			cmd.Printf("%s: %s%s%s\n", board.ID, board.Name, query, builtin)
		}
		if len(result) == 0 {
			cmd.Println("No boards found")
		}
		return nil
	}
	if name == "create" {
		expression, _ := cmd.Flags().GetString("query")
		board, err := client.CreateBoard(cmd.Context(), args[0], expression, actor)
		if err != nil {
			return err
		}
		return printGitHubBoardSaved(cmd, board, "Created")
	}
	board, err := client.GetBoard(cmd.Context(), args[0])
	if err != nil {
		return err
	}
	switch name {
	case "show":
		snapshot, err := issuestore.ReadGitHubBoardSnapshotForActor(cmd.Context(), client, board, actor, true)
		if err != nil {
			return err
		}
		candidates := []models.Issue{}
		for _, issue := range snapshot.Candidates {
			if statuses[issue.Status] {
				candidates = append(candidates, issue)
			}
		}
		views, err := ghstore.ApplyBoardPositions(board, candidates)
		if err != nil {
			return err
		}
		if jsonMode(cmd) {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(views)
		}
		cmd.Printf("Board: %s (%s)\n", board.Name, board.ID)
		if board.Query != "" {
			cmd.Printf("Query: %s\n", board.Query)
		}
		cmd.Println()
		for i, view := range views {
			indicator := fmt.Sprintf("(%d) ", i+1)
			if view.HasPosition {
				indicator = fmt.Sprintf("[%d] ", view.Position)
			}
			cmd.Printf("%s%s %s %s [%s] %s\n", indicator, view.Issue.ID, getStatusIcon(view.Issue.Status), view.Issue.Priority, view.Issue.Type, view.Issue.Title)
		}
		if len(views) == 0 {
			cmd.Println("No issues on this board")
		}
		return nil
	case "edit":
		// The virtual builtin may only be materialized for an explicit mode change.
		if board.Number == 0 && changes.Name == nil && changes.Query == nil && mode != nil {
			board, err = client.MaterializeBuiltinBoardObserved(cmd.Context(), board, actor)
			if err != nil {
				return err
			}
		}
		updated, err := client.UpdateBoardConfigObserved(cmd.Context(), board, changes, mode, actor)
		if err != nil {
			return err
		}
		return printGitHubBoardSaved(cmd, updated, "Updated")
	case "delete":
		deleted, err := client.DeleteBoardObserved(cmd.Context(), board, actor, "")
		if err != nil {
			return err
		}
		return printGitHubBoardSaved(cmd, deleted, "Deleted")
	case "move", "unposition":
		if name == "unposition" {
			number, err := ghstore.Number(args[1])
			if err != nil {
				return err
			}
			// Permit cleaning a saved position for a deleted/missing task.
			updated, err := client.RemoveBoardPositionObserved(cmd.Context(), board, fmt.Sprintf("gh-%d", number), actor)
			if err != nil {
				return err
			}
			return printGitHubBoardSaved(cmd, updated, "Removed explicit position on")
		}
		issue, err := client.Get(cmd.Context(), args[1])
		if err != nil {
			return err
		}
		materialized := false
		if board.Number == 0 {
			board, err = client.MaterializeBuiltinBoardObserved(cmd.Context(), board, actor)
			if err != nil {
				return err
			}
			materialized = true
		}
		updated, err := client.MoveBoardPositionObserved(cmd.Context(), board, issue.ID, position, actor)
		if err != nil {
			if materialized {
				return fmt.Errorf("builtin board was saved, but position write failed; inspect GitHub before retrying: %w", err)
			}
			return err
		}
		return printGitHubBoardSaved(cmd, updated, "Set position on")
	}
	return fmt.Errorf("unsupported GitHub board command: %s", name)
}

func printGitHubBoardSaved(cmd *cobra.Command, board *ghstore.BoardRecord, action string) error {
	if jsonMode(cmd) {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(board.Board)
	}
	cmd.Printf("%s board %s (%s)\n", action, board.Name, board.ID)
	return nil
}

package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/ghcontext"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
	"github.com/spf13/cobra"
)

type githubActionHistoryRow struct {
	models.ActionLog
	OperationID  string                     `json:"operation_id"`
	Phase        string                     `json:"phase"`
	CarrierID    string                     `json:"carrier_id,omitempty"`
	Author       string                     `json:"author"`
	URL          string                     `json:"url"`
	DataFormat   string                     `json:"data_format"`
	HistoryScope string                     `json:"history_scope"`
	CreateInput  *ghstore.ActionCreateInput `json:"create_input,omitempty"`
}

func init() {
	for _, command := range []*cobra.Command{lastCmd, undoCmd} {
		local, undo := command.RunE, command == undoCmd
		command.RunE = func(cmd *cobra.Command, args []string) error {
			handled, err := gitHubActionHistoryCommand(cmd, undo)
			if handled {
				return err
			}
			return local(cmd, args)
		}
	}
}

func gitHubActionHistoryCommand(cmd *cobra.Command, undo bool) (bool, error) {
	cfg, err := config.Load(getBaseDir())
	if err != nil {
		return true, err
	}
	kind, err := config.Store(cfg)
	if err != nil {
		return true, err
	}
	if kind == config.StoreSQLite {
		return false, nil
	}
	cmd.SetOut(cmd.OutOrStdout())
	cmd.SilenceUsage = true
	limit := 10
	if undo {
		list, _ := cmd.Flags().GetBool("list")
		if !list {
			return true, withErrorCode("unsupported_operation", fmt.Errorf("gh-issue undo execution is not implemented yet; use undo --list to inspect tracked actions; no GitHub or SQLite write attempted"))
		}
	} else {
		limit, _ = cmd.Flags().GetInt("n")
		if limit < 1 {
			return true, fmt.Errorf("last --n must be at least 1")
		}
	}
	client, err := ghstore.Open(cmd.Context(), getBaseDir(), cfg.GitHub)
	if err != nil {
		return true, err
	}
	dir, err := gitHubContextDirectory()
	if err != nil {
		return true, err
	}
	scope, err := ghcontext.Resolve(cmd.Context(), dir, cfg.GitHub.Repo)
	if err != nil {
		return true, err
	}
	state, err := scope.Update(cmd.Context(), nil)
	if err != nil {
		return true, err
	}
	snapshot, err := client.ReadSnapshot(cmd.Context(), true)
	if err != nil {
		return true, err
	}
	actions, err := snapshot.ActionHistory(state.Session.ID)
	if err != nil {
		return true, err
	}
	if len(actions) > limit {
		actions = actions[:limit]
	}
	rows, err := githubActionHistoryRows(actions)
	if err != nil {
		return true, err
	}
	// Keep the established array contract even for an empty result. The scope
	// note goes to stderr so an empty array cannot imply lossless legacy history.
	cmd.PrintErrln("History scope: tracked td action journals for this session; pre-journal/native changes are not reconstructed. CLI write journaling and undo execution are not fully integrated yet.")
	if jsonMode(cmd) {
		return true, json.NewEncoder(cmd.OutOrStdout()).Encode(rows)
	}
	if len(rows) == 0 {
		cmd.Printf("No tracked actions in current session (%s)\n", state.Session.ID)
		return true, nil
	}
	for _, row := range rows {
		target := row.EntityID
		if target == "" {
			target = "new issue"
		}
		cmd.Printf("%s %s %s (%s) [%s]\n", row.ActionType, row.EntityType, target, formatTimeAgo(row.Timestamp), row.Phase)
	}
	return true, nil
}

func githubActionHistoryRows(actions []ghstore.ActionJournalRecord) ([]githubActionHistoryRow, error) {
	rows := make([]githubActionHistoryRow, 0, len(actions))
	stateJSON := func(state *ghstore.ActionIssueState) (string, error) {
		if state == nil {
			return "", nil
		}
		data, err := json.Marshal(state)
		return string(data), err
	}
	for _, action := range actions {
		before, err := stateJSON(action.Before)
		if err != nil {
			return nil, err
		}
		after, err := stateJSON(action.After)
		if err != nil {
			return nil, err
		}
		rows = append(rows, githubActionHistoryRow{ActionLog: models.ActionLog{ID: action.ID, SessionID: action.SessionID,
			ActionType: models.ActionType(action.Action), EntityType: "issue", EntityID: action.IssueID,
			PreviousData: before, NewData: after, Timestamp: action.CreatedAt, Undone: action.Phase == "undone"},
			OperationID: action.OperationID, Phase: action.Phase, CarrierID: action.CarrierID, Author: action.Author, URL: action.URL,
			DataFormat: "github-native-issue-state-v1", HistoryScope: "td-action-journal", CreateInput: action.CreateInput})
	}
	return rows, nil
}

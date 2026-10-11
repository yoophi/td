package cmd

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/ghcontext"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
	"github.com/spf13/cobra"
)

type githubActionHistoryRow struct {
	ImportCommand      *ghstore.ActionImportCommand      `json:"import_command,omitempty"`
	ImportForward      *ghstore.ActionImportForward      `json:"import_forward,omitempty"`
	CreationCommand    *ghstore.ActionCreationCommand    `json:"creation_command,omitempty"`
	CommentObservation *ghstore.ActionCommentObservation `json:"comment_observation,omitempty"`
	Group              *ghstore.ActionGroup              `json:"group,omitempty"`
	UpdateObservation  *ghstore.ActionUpdateObservation  `json:"update_observation,omitempty"`
	UpdateIntent       *ghstore.ActionUpdateIntent       `json:"update_intent,omitempty"`
	NativeUndo         *ghstore.ActionNativeUndo         `json:"native_undo,omitempty"`
	models.ActionLog
	OperationID       string                         `json:"operation_id"`
	UndoOperationID   string                         `json:"undo_operation_id,omitempty"`
	Phase             string                         `json:"phase"`
	CarrierID         string                         `json:"carrier_id,omitempty"`
	Author            string                         `json:"author"`
	URL               string                         `json:"url"`
	DataFormat        string                         `json:"data_format"`
	HistoryScope      string                         `json:"history_scope"`
	CreateInput       *ghstore.ActionCreateInput     `json:"create_input,omitempty"`
	TargetIssueID     string                         `json:"target_issue_id,omitempty"`
	CommentInput      *ghstore.ActionCommentInput    `json:"comment_input,omitempty"`
	CommentAfter      *ghstore.ActionCommentState    `json:"comment_after,omitempty"`
	CommentBefore     *ghstore.ActionCommentState    `json:"comment_before,omitempty"`
	UndoObservation   *ghstore.ActionUndoObservation `json:"undo_observation,omitempty"`
	Reconciliation    *ghstore.ActionReconciliation  `json:"reconciliation,omitempty"`
	CommentDataFormat string                         `json:"comment_data_format,omitempty"`
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
	reconcile, _ := cmd.Flags().GetString("reconcile")
	apply, _ := cmd.Flags().GetBool("apply")
	list, _ := cmd.Flags().GetBool("list")
	if apply && reconcile == "" {
		return true, fmt.Errorf("--apply requires --reconcile ghc-ID")
	}
	if reconcile != "" && list {
		return true, fmt.Errorf("--reconcile and --list cannot be combined")
	}
	if kind == config.StoreSQLite {
		if reconcile != "" {
			return true, fmt.Errorf("--reconcile requires store=gh-issue; no SQLite write attempted")
		}
		return false, nil
	}
	cmd.SetOut(cmd.OutOrStdout())
	cmd.SilenceUsage = true
	limit := 10
	executeUndo := false
	if undo {
		list, _ := cmd.Flags().GetBool("list")
		executeUndo = !list
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
	if reconcile != "" {
		result, err := client.ReconcileAction(cmd.Context(), reconcile, state.Session.ID, apply)
		if err != nil {
			var unsupported *ghstore.UndoScopeError
			var conflict *ghstore.ConflictError
			if errors.As(err, &unsupported) {
				return true, withErrorCode("unsupported_operation", err)
			}
			if errors.As(err, &conflict) {
				return true, withErrorCode("conflict", err)
			}
			return true, err
		}
		if jsonMode(cmd) {
			return true, json.NewEncoder(cmd.OutOrStdout()).Encode(result)
		}
		if result.Journal.ImportCommand != nil {
			cmd.Printf("Import command %s: phase=%s, partial=%t, receipt saved=%t, already reconciled=%t; no target writes replayed\n", result.Journal.ID, result.Journal.Phase, result.Partial, result.Applied, result.AlreadyReconciled)
			return true, nil
		}
		if result.Journal.Group != nil {
			cmd.Printf("Group %s: members=%d, phase=%s, group receipt saved=%t; member targets were not replayed by reconciliation\n", result.Journal.ID, len(result.GroupMembers), result.Journal.Phase, result.GroupReceiptSaved)
		}
		if result.Journal.CreationCommand != nil {
			cmd.Printf("Creation command %s: phase=%s, partial=%t; missing effects were not replayed\n", result.Journal.ID, result.Journal.Phase, result.Partial)
			if result.Journal.Phase == "undo_prepared" && result.Journal.CreationCommand.Inverse.Checkpoint == nil {
				cmd.Println("Inspect the recovered result, then run td undo to continue the remaining unattempted effects.")
			}
			if result.Journal.Phase == "undone" {
				cmd.Println("Created task logically deleted; GitHub issue identity and history retained.")
			}
		}
		if result.ForwardOutcome != nil {
			cmd.Printf("Forward result: proof=%s, target write verified=%t, causal undo chain eligible=%t\n", result.ForwardOutcome.Proof, result.ForwardOutcome.TargetWriteVerified, result.ForwardOutcome.CausalUndoChainEligible)
		}
		if result.Compensation != nil {
			cmd.Printf("Native compensation: new review required=%t; native events retained; causal undo chain eligible=false\n", result.Compensation.RequiresNewReview)
		}
		cmd.Printf("%s: target %s observed; target write replayed=false, receipt saved=%t, already reconciled=%t\n", result.Journal.ID, result.Issue.ID, result.Applied, result.AlreadyReconciled)
		return true, nil
	}
	ctx := cmd.Context()
	if executeUndo {
		ctx = ghstore.WithFreshSnapshot(ctx)
	}
	snapshot, err := client.ReadSnapshot(ctx, true)
	if err != nil {
		return true, err
	}
	actions, err := snapshot.ActionHistory(state.Session.ID)
	if err != nil {
		return true, err
	}
	actions, err = ghstore.CollapseActionGroups(actions)
	if err != nil {
		return true, err
	}
	if executeUndo {
		return true, executeGitHubUndo(cmd, client, actions, state.Session.ID)
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
	cmd.PrintErrln("History scope: tracked td action journals for this session; pre-journal/native changes are not reconstructed. Journaling across all writers and workflow/multi-target undo are not fully integrated yet.")
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
		phase := row.Phase
		if row.CreationCommand != nil && row.CreationCommand.Partial {
			phase += ", partial creation"
		}
		cmd.Printf("%s %s %s (%s) [%s]\n", row.ActionType, row.EntityType, target, formatTimeAgo(row.Timestamp), phase)
	}
	return true, nil
}

func executeGitHubUndo(cmd *cobra.Command, client *ghstore.Client, actions []ghstore.ActionJournalRecord, sessionID string) error {
	cmd.PrintErrln("History scope: tracked td action journals for this session; pre-journal/native changes are not reconstructed. Workflow/multi-target undo integration remains pending.")
	var selected *ghstore.ActionJournalRecord
	for i := range actions {
		if actions[i].Phase != "undone" {
			selected = &actions[i]
			break
		}
	}
	if selected == nil {
		if jsonMode(cmd) {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"action": "no_tracked_actions", "session_id": sessionID, "history_scope": "td-action-journal"})
		}
		cmd.Println("No tracked actions to undo in current session")
		return nil
	}
	if selected.Phase != "applied" && (selected.Group == nil && selected.CreationCommand == nil || selected.Phase != "undo_prepared") {
		return withErrorCode("conflict", fmt.Errorf("latest tracked action %s is %s with an unverified outcome; inspect GitHub before retrying; no target write attempted", selected.OperationID, selected.Phase))
	}
	if selected.Action == "create" && selected.CreationCommand == nil {
		return withErrorCode("unsupported_operation", fmt.Errorf("creation %s: %s; no target write attempted", selected.OperationID, selected.CreationUndoLimit()))
	}
	result, err := client.UndoAction(cmd.Context(), selected.ID, sessionID)
	if err != nil {
		var unsupported *ghstore.UndoScopeError
		var conflict *ghstore.ConflictError
		if errors.As(err, &unsupported) {
			return withErrorCode("unsupported_operation", err)
		}
		if errors.As(err, &conflict) {
			return withErrorCode("conflict", err)
		}
		return err
	}
	kind, id := result.Journal.EntityIdentity()
	if jsonMode(cmd) {
		out := map[string]any{"action": "undone", "id": id,
			"operation_id": result.Journal.OperationID, "undo_operation_id": result.Journal.UndoOperationID, "receipt_id": result.Journal.ID,
			"already_undone": result.AlreadyUndone, "issue": result.Issue, "history_scope": "td-action-journal"}
		if result.GroupMembers != nil {
			out["group_members"] = result.GroupMembers
		}
		if result.Compensation != nil {
			out["compensation"] = result.Compensation
		}
		if result.UndoOutcome != nil {
			out["undo_outcome"] = result.UndoOutcome
		}
		if result.Journal.CreationCommand != nil {
			out["compensation_kind"] = "logical-delete-created-task"
			out["preserves_issue_history"] = true
		}
		if kind != "issue" {
			out["entity_type"], out["target_issue_id"] = kind, result.Issue.ID
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(out)
	}
	if result.Compensation != nil {
		cmd.Printf("COMPENSATED: %s %s (new review required=%t; native events retained; causal undo chain eligible=false)\n", result.Journal.Action, id, result.Compensation.RequiresNewReview)
		return nil
	}
	if result.Journal.CreationCommand != nil {
		cmd.Printf("COMPENSATED: create %s (relationships restored; task logically deleted; GitHub issue identity and history retained)\n", id)
		return nil
	}
	cmd.Printf("UNDONE: %s %s %s (%s)\n", result.Journal.Action, kind, id, result.Journal.UndoOperationID)
	return nil
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
		entityType, entityID, targetIssueID, commentFormat := "issue", action.IssueID, "", ""
		dataFormat := "github-native-issue-state-v1"
		if action.Group != nil {
			dataFormat = "github-action-group-v1"
		}
		if action.ImportCommand != nil {
			dataFormat = "github-import-command-v1"
		}
		if kind, id := action.EntityIdentity(); kind != "issue" {
			entityType, entityID, targetIssueID = kind, id, action.IssueID
		}
		if action.Action == "comment_create" || action.Action == "comment_delete" {
			entityType, entityID, targetIssueID, commentFormat = "comment", "new comment", action.IssueID, "github-native-comment-state-v1"
			if action.CommentBefore != nil {
				entityID = action.CommentBefore.ID
			}
			if action.CommentAfter != nil {
				entityID = action.CommentAfter.ID
			}
		}
		rows = append(rows, githubActionHistoryRow{ActionLog: models.ActionLog{ID: action.ID, SessionID: action.SessionID,
			ActionType: models.ActionType(action.Action), EntityType: entityType, EntityID: entityID,
			PreviousData: before, NewData: after, Timestamp: action.CreatedAt, Undone: action.Phase == "undone"},
			OperationID: action.OperationID, UndoOperationID: action.UndoOperationID, Phase: action.Phase, CarrierID: action.CarrierID, Author: action.Author, URL: action.URL,
			DataFormat: dataFormat, HistoryScope: "td-action-journal", CreateInput: action.CreateInput, CreationCommand: action.CreationCommand, ImportCommand: action.ImportCommand, ImportForward: action.ImportForward, CommentObservation: action.CommentObservation,
			TargetIssueID: targetIssueID, CommentInput: action.CommentInput, CommentAfter: action.CommentAfter, CommentBefore: action.CommentBefore, Reconciliation: action.Reconciliation, NativeUndo: action.NativeUndo, Group: action.Group, UpdateIntent: action.UpdateIntent, UpdateObservation: action.UpdateObservation, UndoObservation: action.UndoObservation, CommentDataFormat: commentFormat})
	}
	return rows, nil
}

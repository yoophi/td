package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/marcus/td/internal/backendstore"
	"github.com/spf13/cobra"
)

var errBackendNoUndoAction = errors.New("no reversible backend action in current session")

func backendReversible(op backendstore.Operation) bool {
	if !op.HasEffects || op.Undone {
		return false
	}
	switch op.Kind {
	case "operation.undo", "project.create", "session.create", "session.heartbeat":
		return false
	}
	return true
}

func runBackendActionHistory(cmd *cobra.Command, undo bool) error {
	cmd.SilenceUsage = true
	cmd.SetOut(cmd.OutOrStdout())
	c, e := openBackendCommand(cmd)
	if e != nil {
		return e
	}
	if !undo {
		n, _ := cmd.Flags().GetInt("n")
		if n < 1 {
			return fmt.Errorf("last --n must be at least 1")
		}
		rows, e := c.Operations(cmd.Context())
		if e != nil {
			return e
		}
		if len(rows) > n {
			rows = rows[:n]
		}
		if jsonMode(cmd) {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(rows)
		}
		if len(rows) == 0 {
			_, e = fmt.Fprintf(cmd.OutOrStdout(), "No tracked actions in current session (%s)\n", c.SessionID)
			return e
		}
		for _, row := range rows {
			state := ""
			if row.Undone {
				state = " [undone]"
			}
			if _, e := fmt.Fprintf(cmd.OutOrStdout(), "%s %s (%s)%s\n", row.Kind, row.Path, formatTimeAgo(row.CreatedAt), state); e != nil {
				return e
			}
		}
		return nil
	}
	list, _ := cmd.Flags().GetBool("list")
	if list {
		rows, e := c.Operations(cmd.Context())
		if e != nil {
			return e
		}
		items := []backendstore.Operation{}
		for _, row := range rows {
			if row.HasEffects && row.Kind != "operation.undo" {
				items = append(items, row)
			}
			if len(items) == 10 {
				break
			}
		}
		if jsonMode(cmd) {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(items)
		}
		if len(items) == 0 {
			_, e = fmt.Fprintln(cmd.OutOrStdout(), "No actions to undo")
			return e
		}
		for _, item := range items {
			state := ""
			if item.Undone {
				state = " [undone]"
			}
			if _, e := fmt.Fprintf(cmd.OutOrStdout(), "%s %s (%s)%s\n", item.Kind, item.Path, formatTimeAgo(item.CreatedAt), state); e != nil {
				return e
			}
		}
		return nil
	}
	var selected string
	var reply struct {
		Undone         bool                 `json:"undone"`
		AffectedIssues []backendstore.Issue `json:"affected_issues"`
	}
	e = c.RunIntent(cmd.Context(), backendInvocation(cmd, nil, "undo"), func() (backendstore.MutationRequest, error) {
		rows, e := c.Operations(cmd.Context())
		if e != nil {
			return backendstore.MutationRequest{}, e
		}
		for _, row := range rows {
			if backendReversible(row) {
				selected = row.ID
				break
			}
		}
		if selected == "" {
			return backendstore.MutationRequest{}, errBackendNoUndoAction
		}
		body, e := json.Marshal(map[string]string{"reason": "Undo requested by td CLI"})
		if e != nil {
			return backendstore.MutationRequest{}, e
		}
		return backendstore.MutationRequest{Method: "POST", Resource: "/operations/" + selected + "/undo", Body: body}, nil
	}, &reply, func() error {
		if !reply.Undone || reply.AffectedIssues == nil {
			return fmt.Errorf("backend did not confirm undo")
		}
		for _, issue := range reply.AffectedIssues {
			if e := backendstore.ValidateIssue(issue); e != nil {
				return e
			}
		}
		return nil
	})
	if errors.Is(e, errBackendNoUndoAction) {
		if jsonMode(cmd) {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"action": "no_tracked_actions", "session_id": c.SessionID})
		}
		_, e = fmt.Fprintf(cmd.OutOrStdout(), "No tracked actions to undo in current session (%s)\n", c.SessionID)
		return e
	}
	if e != nil {
		return e
	}
	if jsonMode(cmd) {
		result := map[string]any{"action": "undone", "affected_issues": reply.AffectedIssues}
		if selected != "" {
			result["operation_id"] = selected
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}
	ids := []string{}
	for _, issue := range reply.AffectedIssues {
		ids = append(ids, issue.ID)
	}
	_, e = fmt.Fprintf(cmd.OutOrStdout(), "UNDONE %s\n", strings.Join(ids, ", "))
	return e
}

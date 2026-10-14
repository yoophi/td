package cmd

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/output"
	"github.com/spf13/cobra"
)

func init() {
	for _, command := range []*cobra.Command{wsListCmd, wsCurrentCmd, wsShowCmd} {
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
				return runBackendWSRead(target, cmd, args)
			}
			return local(cmd, args)
		}
	}
}

func runBackendWSRead(target, cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	c, e := openBackendCommand(cmd)
	if e != nil {
		return e
	}
	if target == wsListCmd {
		rows, e := c.WorkSessions(cmd.Context())
		if e != nil {
			return e
		}
		slices.SortFunc(rows, func(a, b backendstore.WorkSession) int { return b.StartedAt.Compare(a.StartedAt) })
		if len(rows) > 20 {
			rows = rows[:20]
		}
		type entry struct {
			backendstore.WorkSession
			Issues []string `json:"issues"`
			Status string   `json:"status"`
		}
		items := []entry{}
		for _, row := range rows {
			detail, e := c.WorkSession(cmd.Context(), row.ID)
			if e != nil {
				return e
			}
			status := "active"
			if row.EndedAt != nil {
				status = "completed"
			}
			items = append(items, entry{row, detail.IssueIDs, status})
		}
		if jsonMode(cmd) {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(items)
		}
		if len(items) == 0 {
			_, e = fmt.Fprintln(cmd.OutOrStdout(), "No work sessions")
			return e
		}
		for i, row := range rows {
			if _, e = fmt.Fprintf(cmd.OutOrStdout(), "%s  %q  %s  %s  [%s]\n", row.ID, row.Name, output.FormatTimeAgo(row.StartedAt), strings.Join(items[i].Issues, ","), items[i].Status); e != nil {
				return e
			}
		}
		return nil
	}
	id := ""
	if target == wsCurrentCmd {
		id, e = c.ActiveWorkSessionID(cmd.Context())
		if e != nil {
			return e
		}
		if id == "" {
			if jsonMode(cmd) {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"work_session": nil, "issues": []string{}, "activities": []any{}})
			}
			_, e = fmt.Fprintln(cmd.OutOrStdout(), "No active work session")
			return e
		}
	} else {
		rows, e := c.WorkSessions(cmd.Context())
		if e != nil {
			return e
		}
		for _, row := range rows {
			if row.ID == args[0] || strings.HasPrefix(row.ID, args[0]) {
				if id != "" && id != row.ID {
					return fmt.Errorf("ambiguous work session ID %q", args[0])
				}
				id = row.ID
			}
		}
		if id == "" {
			return fmt.Errorf("work session %q not found", args[0])
		}
	}
	detail, e := c.WorkSession(cmd.Context(), id)
	if e != nil {
		return e
	}
	activities, e := c.WorkSessionActivity(cmd.Context(), id)
	if e != nil {
		return e
	}
	if jsonMode(cmd) {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"work_session": detail.WorkSession, "issues": detail.IssueIDs, "activities": activities})
	}
	if _, e = fmt.Fprintf(cmd.OutOrStdout(), "WORK SESSION: %s %q\n", detail.WorkSession.ID, detail.WorkSession.Name); e != nil {
		return e
	}
	if _, e = fmt.Fprintf(cmd.OutOrStdout(), "%s\n\nTAGGED ISSUES:\n", output.FormatTimeAgo(detail.WorkSession.StartedAt)); e != nil {
		return e
	}
	for _, issueID := range detail.IssueIDs {
		if _, e = fmt.Fprintln(cmd.OutOrStdout(), issueID); e != nil {
			return e
		}
	}
	full, _ := cmd.Flags().GetBool("full")
	display := activities
	if target == wsCurrentCmd && len(display) > 5 {
		display = display[len(display)-5:]
	}
	for _, activity := range display {
		if target == wsShowCmd && !full && activity.Kind != "handoff" {
			continue
		}
		if _, e = fmt.Fprintf(cmd.OutOrStdout(), "  [%s] %s %s [%s] %s\n", activity.CreatedAt.Format("2006-01-02 15:04"), activity.Kind, activity.IssueID, activity.LogType, output.SanitizeIssueText(activity.Message)); e != nil {
			return e
		}
		if activity.Kind == "handoff" {
			if _, e = fmt.Fprintf(cmd.OutOrStdout(), "    done=%v remaining=%v decisions=%v uncertain=%v\n", sanitizeContextItems(activity.Done), sanitizeContextItems(activity.Remaining), sanitizeContextItems(activity.Decisions), sanitizeContextItems(activity.Uncertain)); e != nil {
				return e
			}
		}
	}
	return nil
}

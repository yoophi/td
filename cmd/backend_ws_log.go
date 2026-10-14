package cmd

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/models"
	"github.com/spf13/cobra"
)

func init() {
	local := wsLogCmd.RunE
	wsLogCmd.RunE = func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(getBaseDir())
		if err != nil {
			return err
		}
		kind, err := config.Store(cfg)
		if err != nil {
			return err
		}
		if kind == config.StoreBackend {
			return runBackendWSLog(cmd, args)
		}
		return local(cmd, args)
	}
}

func runBackendWSLog(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	if strings.TrimSpace(args[0]) == "" {
		return fmt.Errorf("work-session log message must not be empty")
	}
	logType := models.LogTypeProgress
	selected := 0
	for _, name := range []string{"blocker", "decision", "hypothesis", "tried", "result"} {
		set, _ := cmd.Flags().GetBool(name)
		if set {
			logType = models.LogType(name)
			selected++
		}
	}
	if selected > 1 {
		return fmt.Errorf("choose only one work-session log type")
	}
	c, err := openBackendCommand(cmd)
	if err != nil {
		return err
	}
	wsID, err := c.ActiveWorkSessionID(cmd.Context())
	if err != nil {
		return err
	}
	if wsID == "" {
		return fmt.Errorf("no active work session")
	}
	detail, err := c.WorkSession(cmd.Context(), wsID)
	if err != nil {
		return err
	}
	targets := slices.Clone(detail.IssueIDs)
	if only, _ := cmd.Flags().GetString("only"); only != "" {
		rows, err := c.Client.ListIssues(cmd.Context(), c.ProjectID, c.SessionID, true)
		if err != nil {
			return err
		}
		id, err := resolveBackendID(rows, only)
		if err != nil {
			return err
		}
		targets = []string{id}
	}
	if len(targets) == 0 {
		var reply struct {
			Log backendLog `json:"log"`
		}
		err := c.RunIntent(cmd.Context(), backendInvocation(cmd, args, "ws.log.session"), func() (backendstore.MutationRequest, error) {
			active, err := c.WorkSession(cmd.Context(), wsID)
			if err != nil {
				return backendstore.MutationRequest{}, err
			}
			if active.WorkSession.SessionID != c.SessionID || active.WorkSession.EndedAt != nil || len(active.IssueIDs) != 0 {
				return backendstore.MutationRequest{}, fmt.Errorf("active work session or tags changed; reread before retrying")
			}
			body, err := json.Marshal(map[string]string{"message": args[0], "type": string(logType)})
			if err != nil {
				return backendstore.MutationRequest{}, err
			}
			return backendstore.MutationRequest{Method: "POST", Resource: "/work-sessions/" + wsID + "/logs", Body: body}, nil
		}, &reply, func() error {
			row := reply.Log
			if row.ID == "" || row.IssueID != "" || row.SessionID != c.SessionID || row.WorkSessionID != wsID || row.Message != strings.TrimSpace(args[0]) || row.Type != logType || row.Timestamp.IsZero() {
				return fmt.Errorf("invalid backend work-session log response")
			}
			return nil
		})
		if err != nil {
			return err
		}
		if jsonMode(cmd) {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"action": "logged_work_session", "work_session_id": wsID, "issues": []string{}, "logs": []backendLog{reply.Log}})
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "LOGGED %s [%s] (no issues tagged)\n", wsID, logType)
		return err
	}
	// Validate the entire target set before the first durable mutation.
	for _, id := range targets {
		if _, err := c.Client.GetIssue(cmd.Context(), c.ProjectID, c.SessionID, id); err != nil {
			return err
		}
	}
	completed := []string{}
	logs := []backendLog{}
	for index, id := range targets {
		var reply struct {
			Log backendLog `json:"log"`
		}
		identity := struct {
			Command any
			Index   int
			IssueID string
		}{backendInvocation(cmd, args, "ws.log"), index, id}
		err := c.RunIntent(cmd.Context(), identity, func() (backendstore.MutationRequest, error) {
			current, err := c.Client.GetIssue(cmd.Context(), c.ProjectID, c.SessionID, id)
			if err != nil {
				return backendstore.MutationRequest{}, err
			}
			active, err := c.WorkSession(cmd.Context(), wsID)
			if err != nil {
				return backendstore.MutationRequest{}, err
			}
			if active.WorkSession.SessionID != c.SessionID || active.WorkSession.EndedAt != nil || !slices.Equal(active.IssueIDs, detail.IssueIDs) {
				return backendstore.MutationRequest{}, fmt.Errorf("active work session or tags changed; reread before retrying")
			}
			body, err := json.Marshal(map[string]string{"message": args[0], "type": string(logType), "work_session_id": wsID})
			if err != nil {
				return backendstore.MutationRequest{}, err
			}
			return backendstore.MutationRequest{Method: "POST", Resource: "/issues/" + id + "/logs", Revision: current.Revision, Body: body}, nil
		}, &reply, func() error {
			row := reply.Log
			if row.ID == "" || row.IssueID != id || row.SessionID != c.SessionID || row.WorkSessionID != wsID || row.Message != args[0] || row.Type != logType || row.Timestamp.IsZero() {
				return fmt.Errorf("invalid backend work-session log response for %s", id)
			}
			return nil
		})
		if err != nil {
			if len(completed) > 0 {
				return fmt.Errorf("ws log recorded for %s; stopped at %s (earlier logs remain; retry the same command): %w", strings.Join(completed, ", "), id, err)
			}
			return fmt.Errorf("ws log stopped at %s: %w", id, err)
		}
		completed = append(completed, id)
		logs = append(logs, reply.Log)
	}
	if jsonMode(cmd) {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"action": "logged_work_session", "work_session_id": wsID, "issues": completed, "logs": logs})
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "LOGGED %s [%s] → %s\n", wsID, logType, strings.Join(completed, ", "))
	return err
}

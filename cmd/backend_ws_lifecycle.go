package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/internal/config"
	"github.com/spf13/cobra"
)

func init() {
	for _, command := range []*cobra.Command{wsStartCmd, wsEndCmd} {
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
				return runBackendWSLifecycle(target, cmd, args)
			}
			return local(cmd, args)
		}
	}
}

func runBackendWSLifecycle(target, cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	c, e := openBackendCommand(cmd)
	if e != nil {
		return e
	}
	var selected string
	var reply struct {
		WorkSession backendstore.WorkSession `json:"work_session"`
	}
	e = c.RunIntent(cmd.Context(), backendInvocation(cmd, args, "ws."+target.Name()), func() (backendstore.MutationRequest, error) {
		active, e := c.ActiveWorkSessionID(cmd.Context())
		if e != nil {
			return backendstore.MutationRequest{}, e
		}
		if target == wsStartCmd && active != "" {
			return backendstore.MutationRequest{}, fmt.Errorf("work session already active: %s", active)
		}
		if target == wsEndCmd && active == "" {
			return backendstore.MutationRequest{}, fmt.Errorf("no active work session")
		}
		selected = active
		snapshot, e := gitHubSnapshot(cmd.Context(), c.Worktree)
		if e != nil {
			return backendstore.MutationRequest{}, e
		}
		resource := "/work-sessions"
		payload := map[string]any{}
		if target == wsEndCmd {
			resource += "/" + active + "/end"
			payload = map[string]any{"end_sha": snapshot.CommitSHA}
		} else {
			payload = map[string]any{"name": args[0], "start_sha": snapshot.CommitSHA}
		}
		body, e := json.Marshal(payload)
		if e != nil {
			return backendstore.MutationRequest{}, e
		}
		return backendstore.MutationRequest{Method: "POST", Resource: resource, Body: body}, nil
	}, &reply, func() error {
		if e := backendstore.ValidateWorkSession(reply.WorkSession); e != nil {
			return e
		}
		if reply.WorkSession.SessionID != c.SessionID || selected != "" && reply.WorkSession.ID != selected {
			return fmt.Errorf("backend returned a different work session")
		}
		if target == wsStartCmd && reply.WorkSession.EndedAt != nil || target == wsEndCmd && reply.WorkSession.EndedAt == nil {
			return fmt.Errorf("backend returned wrong work session state")
		}
		return nil
	})
	if e != nil {
		return e
	}
	if jsonMode(cmd) {
		action := "started_work_session"
		if target == wsEndCmd {
			action = "ended_work_session"
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"action": action, "work_session": reply.WorkSession, "issues": []string{}})
	}
	if target == wsStartCmd {
		_, e = fmt.Fprintf(cmd.OutOrStdout(), "WORK SESSION STARTED: %s\nName: %s\n", reply.WorkSession.ID, reply.WorkSession.Name)
	} else {
		_, e = fmt.Fprintf(cmd.OutOrStdout(), "WORK SESSION ENDED: %s (no handoff recorded)\n", reply.WorkSession.ID)
	}
	return e
}

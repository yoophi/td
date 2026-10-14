package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/models"
	"github.com/spf13/cobra"
)

var errBackendWSTagsUnchanged = errors.New("work session tags already have requested state")

func init() {
	for _, command := range []*cobra.Command{wsTagCmd, wsUntagCmd} {
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
				return runBackendWSTags(target, cmd, args)
			}
			return local(cmd, args)
		}
	}
}

func runBackendWSTags(target, cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	c, e := openBackendCommand(cmd)
	if e != nil {
		return e
	}
	wsID, e := c.ActiveWorkSessionID(cmd.Context())
	if e != nil {
		return e
	}
	if wsID == "" {
		return fmt.Errorf("no active work session. Run 'td ws start <name>' first")
	}
	rows, e := c.Client.ListIssues(cmd.Context(), c.ProjectID, c.SessionID, false)
	if e != nil {
		return e
	}
	requested := []string{}
	for _, raw := range args {
		id, e := resolveBackendID(rows, raw)
		if e != nil {
			return e
		}
		if !slices.Contains(requested, id) {
			requested = append(requested, id)
		}
	}
	noStart, _ := cmd.Flags().GetBool("no-start")
	if target == wsTagCmd && !noStart {
		completed := []string{}
		for index, id := range requested {
			var reply struct {
				Issue backendstore.Issue `json:"issue"`
			}
			observed := backendstore.Issue{}
			identity := struct {
				Command any
				Index   int
				IssueID string
			}{backendInvocation(cmd, args, "ws.tag.start"), index, id}
			e = c.RunIntent(cmd.Context(), identity, func() (backendstore.MutationRequest, error) {
				issue, e := c.Client.GetIssue(cmd.Context(), c.ProjectID, c.SessionID, id)
				if e != nil {
					return backendstore.MutationRequest{}, e
				}
				observed = issue
				if issue.Status != models.StatusOpen {
					return backendstore.MutationRequest{}, errBackendTransitionUnchanged
				}
				snapshot, e := gitHubSnapshot(cmd.Context(), c.Worktree)
				if e != nil {
					return backendstore.MutationRequest{}, e
				}
				body, e := json.Marshal(map[string]any{"reason": "Started via work session " + wsID, "snapshot": map[string]any{"commit_sha": snapshot.CommitSHA, "branch": snapshot.Branch, "dirty_files": snapshot.DirtyFiles}})
				if e != nil {
					return backendstore.MutationRequest{}, e
				}
				return backendstore.MutationRequest{Method: "POST", Resource: "/issues/" + id + "/start", Revision: issue.Revision, Body: body}, nil
			}, &reply, func() error {
				if e := backendstore.ValidateIssue(reply.Issue); e != nil {
					return e
				}
				if reply.Issue.ID != id || reply.Issue.Status != models.StatusInProgress || reply.Issue.ImplementerSession != c.SessionID {
					return fmt.Errorf("backend did not start %s for this Session", id)
				}
				return nil
			})
			if errors.Is(e, errBackendTransitionUnchanged) {
				e = nil
				reply.Issue = observed
			}
			if e != nil {
				if len(completed) == 0 {
					return fmt.Errorf("ws tag stopped at %s: %w", id, e)
				}
				return fmt.Errorf("ws tag starts completed for %s; stopped at %s (earlier changes remain; retry the same command): %w", strings.Join(completed, ", "), id, e)
			}
			completed = append(completed, id)
		}
	}
	var reply struct {
		IssueIDs []string `json:"issue_ids"`
	}
	var desired []string
	e = c.RunIntent(cmd.Context(), backendInvocation(cmd, args, "ws."+target.Name()+".tags"), func() (backendstore.MutationRequest, error) {
		detail, e := c.WorkSession(cmd.Context(), wsID)
		if e != nil {
			return backendstore.MutationRequest{}, e
		}
		if detail.WorkSession.SessionID != c.SessionID || detail.WorkSession.EndedAt != nil {
			return backendstore.MutationRequest{}, fmt.Errorf("active work session changed")
		}
		desired = append([]string{}, detail.IssueIDs...)
		for _, id := range requested {
			if target == wsTagCmd && !slices.Contains(desired, id) {
				desired = append(desired, id)
			}
			if target == wsUntagCmd {
				desired = slices.DeleteFunc(desired, func(item string) bool { return item == id })
			}
		}
		slices.Sort(desired)
		before := append([]string{}, detail.IssueIDs...)
		slices.Sort(before)
		if slices.Equal(before, desired) {
			return backendstore.MutationRequest{}, errBackendWSTagsUnchanged
		}
		body, e := json.Marshal(map[string]any{"issue_ids": desired})
		if e != nil {
			return backendstore.MutationRequest{}, e
		}
		return backendstore.MutationRequest{Method: "PUT", Resource: "/work-sessions/" + wsID + "/issues", Body: body}, nil
	}, &reply, func() error {
		if reply.IssueIDs == nil {
			return fmt.Errorf("backend did not confirm work session tags")
		}
		for _, id := range requested {
			if slices.Contains(reply.IssueIDs, id) != (target == wsTagCmd) {
				return fmt.Errorf("backend work session tags differ from request")
			}
		}
		if desired != nil {
			actual := append([]string{}, reply.IssueIDs...)
			slices.Sort(actual)
			if !slices.Equal(actual, desired) {
				return fmt.Errorf("backend work session tags differ from observed request")
			}
		}
		return nil
	})
	if errors.Is(e, errBackendWSTagsUnchanged) {
		e = nil
		reply.IssueIDs = desired
	}
	if e != nil {
		return e
	}
	action := "tagged_work_session"
	if target == wsUntagCmd {
		action = "untagged_work_session"
	}
	if jsonMode(cmd) {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"action": action, "work_session": wsID, "issues": requested})
	}
	_, e = fmt.Fprintf(cmd.OutOrStdout(), "%s %s → %s\n", strings.ToUpper(target.Name()), strings.Join(requested, ", "), wsID)
	return e
}

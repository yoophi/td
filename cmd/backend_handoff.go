package cmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/internal/models"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func runBackendHandoff(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	allowed := " json work-dir help refresh done remaining decision uncertain note message "
	var invalid error
	cmd.Flags().Visit(func(f *pflag.Flag) {
		if !strings.Contains(allowed, " "+f.Name+" ") {
			invalid = fmt.Errorf("td-backend handoff does not support --%s", f.Name)
		}
	})
	if invalid != nil {
		return invalid
	}
	if len(args) > 2 {
		return fmt.Errorf("handoff accepts at most Issue ID and message")
	}
	c, e := openBackendCommand(cmd)
	if e != nil {
		return e
	}
	explicitID, message := "", ""
	if len(args) == 2 {
		explicitID, message = args[0], args[1]
	} else if len(args) == 1 {
		if strings.HasPrefix(args[0], "td-") {
			explicitID = args[0]
		} else {
			message = args[0]
		}
	}
	var reply struct {
		Activity models.Activity `json:"activity"`
	}
	e = c.RunIntent(cmd.Context(), backendInvocation(cmd, args, "handoff"), func() (backendstore.MutationRequest, error) {
		rawID := explicitID
		if rawID == "" {
			var e error
			rawID, e = c.Focus(cmd.Context())
			if e != nil {
				return backendstore.MutationRequest{}, e
			}
		}
		if rawID == "" {
			return backendstore.MutationRequest{}, fmt.Errorf("no issue specified and no focused issue")
		}
		input := models.Activity{Kind: "handoff", Message: message}
		if e := readGitHubHandoff(cmd, &input); e != nil {
			return backendstore.MutationRequest{}, e
		}
		rows, e := c.Client.ListIssues(cmd.Context(), c.ProjectID, c.SessionID, true)
		if e != nil {
			return backendstore.MutationRequest{}, e
		}
		id, e := resolveBackendID(rows, rawID)
		if e != nil {
			return backendstore.MutationRequest{}, e
		}
		var revision string
		for _, row := range rows {
			if row.ID == id {
				revision = row.Revision
				break
			}
		}
		for _, part := range []*[]string{&input.Done, &input.Remaining, &input.Decisions, &input.Uncertain} {
			if *part == nil {
				*part = []string{}
			}
		}
		body := map[string]any{"kind": "handoff", "done": input.Done, "remaining": input.Remaining, "decisions": input.Decisions, "uncertain": input.Uncertain}
		ws, e := c.ActiveWorkSessionID(cmd.Context())
		if e != nil {
			return backendstore.MutationRequest{}, e
		}
		if ws != "" {
			body["work_session_id"] = ws
		}
		if snapshot, e := gitHubSnapshot(cmd.Context(), c.Worktree); e == nil {
			body["snapshot"] = map[string]any{"commit_sha": snapshot.CommitSHA, "branch": snapshot.Branch, "dirty_files": snapshot.DirtyFiles}
		}
		raw, e := json.Marshal(body)
		if e != nil {
			return backendstore.MutationRequest{}, e
		}
		return backendstore.MutationRequest{Method: "POST", Resource: "/issues/" + id + "/activity", Revision: revision, Body: raw}, nil
	}, &reply, func() error {
		a := reply.Activity
		if a.ID == "" || !backendstore.ValidIssueID(a.IssueID) || a.Kind != "handoff" || a.SessionID != c.SessionID || a.CreatedAt.IsZero() {
			return fmt.Errorf("invalid backend handoff response")
		}
		requested := strings.TrimSpace(explicitID)
		if requested != "" && !strings.HasPrefix(requested, "td-") {
			requested = "td-" + requested
		}
		if requested != "" && !strings.HasPrefix(a.IssueID, requested) {
			return fmt.Errorf("backend handoff belongs to a different Issue")
		}
		return nil
	})
	if e != nil {
		return e
	}
	if jsonMode(cmd) {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"action": "handoff_recorded", "id": reply.Activity.IssueID, "activity": reply.Activity})
	}
	_, e = fmt.Fprintf(cmd.OutOrStdout(), "HANDOFF RECORDED %s\n", reply.Activity.IssueID)
	return e
}

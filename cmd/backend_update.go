package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/internal/models"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

var errBackendStatusUnchanged = errors.New("issue already has requested status")

func runBackendUpdate(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	if len(args) == 0 {
		return fmt.Errorf("requires at least one issue ID")
	}
	if !cmd.Flags().Changed("status") {
		return runBackendPatch(cmd, args)
	}
	allowed := " status json work-dir help refresh "
	var invalid error
	cmd.Flags().Visit(func(f *pflag.Flag) {
		if !strings.Contains(allowed, " "+f.Name+" ") {
			invalid = fmt.Errorf("td-backend update --status cannot be combined with --%s yet", f.Name)
		}
	})
	if invalid != nil {
		return invalid
	}
	raw, _ := cmd.Flags().GetString("status")
	status := models.NormalizeStatus(raw)
	if !models.IsValidStatus(status) {
		return fmt.Errorf("invalid status %q", raw)
	}
	c, e := openBackendCommand(cmd)
	if e != nil {
		return e
	}
	completed := []string{}
	for index, rawID := range args {
		var reply struct {
			Issue backendstore.Issue `json:"issue"`
			Noop  bool               `json:"noop"`
		}
		var observed backendstore.Issue
		identity := struct {
			Command any
			Index   int
		}{backendInvocation(cmd, args, "update-status"), index}
		e = c.RunIntent(cmd.Context(), identity, func() (backendstore.MutationRequest, error) {
			rows, e := c.Client.ListIssues(cmd.Context(), c.ProjectID, c.SessionID, true)
			if e != nil {
				return backendstore.MutationRequest{}, e
			}
			id, e := resolveBackendID(rows, rawID)
			if e != nil {
				return backendstore.MutationRequest{}, e
			}
			for _, row := range rows {
				if row.ID == id {
					observed = row
					break
				}
			}
			if observed.Status == status {
				return backendstore.MutationRequest{}, errBackendStatusUnchanged
			}
			body, e := json.Marshal(map[string]string{"status": string(status)})
			if e != nil {
				return backendstore.MutationRequest{}, e
			}
			return backendstore.MutationRequest{Method: "POST", Resource: "/issues/" + id + "/status", Revision: observed.Revision, Body: body}, nil
		}, &reply, func() error {
			if e := backendstore.ValidateIssue(reply.Issue); e != nil {
				return e
			}
			if reply.Issue.Status != status {
				return fmt.Errorf("backend status response differs from request")
			}
			requested := strings.TrimSpace(rawID)
			if !strings.HasPrefix(requested, "td-") {
				requested = "td-" + requested
			}
			if !strings.HasPrefix(reply.Issue.ID, requested) {
				return fmt.Errorf("backend returned a different Issue")
			}
			return nil
		})
		noop := errors.Is(e, errBackendStatusUnchanged)
		if noop {
			e = nil
			reply.Issue = observed
		}
		if e != nil {
			if len(completed) > 0 {
				return fmt.Errorf("update completed for %s; stopped at %s (earlier changes remain; retry the same command): %w", strings.Join(completed, ", "), rawID, e)
			}
			return e
		}
		completed = append(completed, reply.Issue.ID)
		if jsonMode(cmd) {
			if e := json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"action": "updated", "issue": reply.Issue.Issue, "noop": noop || reply.Noop}); e != nil {
				return e
			}
		} else {
			if _, e := fmt.Fprintf(cmd.OutOrStdout(), "UPDATED %s\n", reply.Issue.ID); e != nil {
				return e
			}
		}
	}
	return nil
}

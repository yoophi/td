package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/marcus/td/internal/backendstore"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

var errBackendAlreadyDeletedState = errors.New("issue already has requested deletion state")

func runBackendDeletion(cmd *cobra.Command, args []string, deleting bool) error {
	cmd.SilenceUsage = true
	if len(args) == 0 {
		return fmt.Errorf("requires at least one issue ID")
	}
	allowed := " json work-dir help refresh reason "
	if deleting {
		allowed += "force yes "
	}
	var invalid error
	cmd.Flags().Visit(func(f *pflag.Flag) {
		if !strings.Contains(allowed, " "+f.Name+" ") {
			invalid = fmt.Errorf("td-backend does not support --%s for %s", f.Name, cmd.Name())
		}
	})
	if invalid != nil {
		return invalid
	}
	reason, err := deletionReason(cmd)
	if err != nil {
		return err
	}
	c, err := openBackendCommand(cmd)
	if err != nil {
		return err
	}
	operation := "restore"
	label := "RESTORED"
	if deleting {
		operation = "delete"
		label = "DELETED"
	}
	completed := []string{}
	results := []map[string]any{}
	for index, rawID := range args {
		var reply struct {
			Issue   backendstore.Issue `json:"issue"`
			Deleted bool               `json:"deleted"`
		}
		var observed backendstore.Issue
		identity := struct {
			Command any
			Index   int
		}{backendInvocation(cmd, args, operation), index}
		err = c.RunIntent(cmd.Context(), identity, func() (backendstore.MutationRequest, error) {
			rows, e := c.Client.ListIssuesIncludingDeleted(cmd.Context(), c.ProjectID, c.SessionID)
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
			if (observed.DeletedAt != nil) == deleting {
				return backendstore.MutationRequest{}, errBackendAlreadyDeletedState
			}
			method, resource := "DELETE", "/issues/"+id
			if !deleting {
				method = "POST"
				resource += "/restore"
			}
			var body json.RawMessage
			if reason != "" {
				body, e = json.Marshal(map[string]string{"reason": reason})
				if e != nil {
					return backendstore.MutationRequest{}, e
				}
			}
			return backendstore.MutationRequest{Method: method, Resource: resource, Revision: observed.Revision, Body: body}, nil
		}, &reply, func() error {
			if deleting && !reply.Deleted {
				return fmt.Errorf("backend did not confirm deletion")
			}
			if e := backendstore.ValidateIssue(reply.Issue); e != nil {
				return e
			}
			requested := strings.TrimSpace(rawID)
			if !strings.HasPrefix(requested, "td-") {
				requested = "td-" + requested
			}
			if !strings.HasPrefix(reply.Issue.ID, requested) {
				return fmt.Errorf("backend returned a different Issue")
			}
			if (reply.Issue.DeletedAt != nil) != deleting {
				return fmt.Errorf("backend returned wrong deleted state")
			}
			return nil
		})
		noop := errors.Is(err, errBackendAlreadyDeletedState)
		if noop {
			err = nil
			reply.Issue = observed
		}
		if err != nil {
			if len(completed) > 0 {
				return fmt.Errorf("%s completed for %s; stopped at %s (earlier changes remain; retry the same command): %w", operation, strings.Join(completed, ", "), rawID, err)
			}
			return err
		}
		completed = append(completed, reply.Issue.ID)
		results = append(results, map[string]any{"id": reply.Issue.ID, "issue": reply.Issue.Issue, "noop": noop})
		if !jsonMode(cmd) {
			if _, e := fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", label, reply.Issue.ID); e != nil {
				return fmt.Errorf("%s saved for %s, but output failed; inspect current state before retrying: %w", operation, strings.Join(completed, ", "), e)
			}
		}
	}
	if jsonMode(cmd) {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(results)
	}
	return nil
}

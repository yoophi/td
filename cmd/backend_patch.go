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

var errBackendPatchUnchanged = errors.New("issue already has requested field values")

func runBackendPatch(cmd *cobra.Command, args []string) error {
	allowed := " json work-dir help refresh title description desc body description-file acceptance acceptance-file type priority points labels sprint parent append due defer "
	var invalid error
	count := 0
	cmd.Flags().Visit(func(f *pflag.Flag) {
		if !strings.Contains(allowed, " "+f.Name+" ") {
			invalid = fmt.Errorf("td-backend update does not support --%s yet", f.Name)
		}
		if !strings.Contains(" json work-dir help refresh append ", " "+f.Name+" ") {
			count++
		}
	})
	if invalid != nil {
		return invalid
	}
	if count == 0 {
		return fmt.Errorf("td-backend update requires a changed field")
	}
	appendMode, _ := cmd.Flags().GetBool("append")
	if appendMode && len(args) > 1 {
		return fmt.Errorf("td-backend update --append currently requires one Issue")
	}
	c, e := openBackendCommand(cmd)
	if e != nil {
		return e
	}
	completed := []string{}
	for index, rawID := range args {
		var reply struct {
			Issue backendstore.Issue `json:"issue"`
		}
		var observed backendstore.Issue
		identity := struct {
			Command any
			Index   int
		}{backendInvocation(cmd, args, "update-fields"), index}
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
			patch, e := backendCompoundFields(cmd, rows, observed, appendMode)
			if e != nil {
				return backendstore.MutationRequest{}, e
			}
			if len(patch) == 0 {
				return backendstore.MutationRequest{}, errBackendPatchUnchanged
			}
			body, e := json.Marshal(patch)
			if e != nil {
				return backendstore.MutationRequest{}, e
			}
			return backendstore.MutationRequest{Method: "PATCH", Resource: "/issues/" + id, Revision: observed.Revision, Body: body}, nil
		}, &reply, func() error {
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
			return nil
		})
		noop := errors.Is(e, errBackendPatchUnchanged)
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
			if e := json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"action": "updated", "issue": reply.Issue.Issue, "noop": noop}); e != nil {
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

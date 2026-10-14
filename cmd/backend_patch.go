package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/internal/dateparse"
	"github.com/marcus/td/internal/models"
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
			patch := map[string]any{}
			changed := false
			put := func(name string, value any, same bool) {
				patch[name] = value
				if !same {
					changed = true
				}
			}
			if cmd.Flags().Changed("title") {
				v, _ := cmd.Flags().GetString("title")
				put("title", v, v == observed.Title)
			}
			description, provided, stdinUsed, e := resolveRichTextField(cmd, []string{"description", "desc", "body"}, "description-file", false)
			if e != nil {
				return backendstore.MutationRequest{}, e
			}
			if provided {
				if appendMode && observed.Description != "" && description != "" {
					description = observed.Description + "\n\n" + description
				}
				put("description", description, description == observed.Description)
			}
			acceptance, provided, _, e := resolveRichTextField(cmd, []string{"acceptance"}, "acceptance-file", stdinUsed)
			if e != nil {
				return backendstore.MutationRequest{}, e
			}
			if provided {
				if appendMode && observed.Acceptance != "" && acceptance != "" {
					acceptance = observed.Acceptance + "\n\n" + acceptance
				}
				put("acceptance", acceptance, acceptance == observed.Acceptance)
			}
			if cmd.Flags().Changed("type") {
				raw, _ := cmd.Flags().GetString("type")
				v := models.NormalizeType(raw)
				if !models.IsValidType(v) {
					return backendstore.MutationRequest{}, fmt.Errorf("invalid type %q", raw)
				}
				put("type", v, v == observed.Type)
			}
			if cmd.Flags().Changed("priority") {
				raw, _ := cmd.Flags().GetString("priority")
				v := models.NormalizePriority(raw)
				if !models.IsValidPriority(v) {
					return backendstore.MutationRequest{}, fmt.Errorf("invalid priority %q", raw)
				}
				put("priority", v, v == observed.Priority)
			}
			if cmd.Flags().Changed("points") {
				v, _ := cmd.Flags().GetInt("points")
				if v != 0 && !models.IsValidPoints(v) {
					return backendstore.MutationRequest{}, fmt.Errorf("invalid points %d", v)
				}
				put("points", v, v == observed.Points)
			}
			if cmd.Flags().Changed("labels") {
				raw, _ := cmd.Flags().GetStringArray("labels")
				labels := mergeMultiValueFlag(raw)
				slices.Sort(labels)
				labels = slices.Compact(labels)
				current := append([]string{}, observed.Labels...)
				slices.Sort(current)
				current = slices.Compact(current)
				put("labels", labels, slices.Equal(labels, current))
			}
			if cmd.Flags().Changed("sprint") {
				v, _ := cmd.Flags().GetString("sprint")
				put("sprint", v, v == observed.Sprint)
			}
			if cmd.Flags().Changed("parent") {
				v, _ := cmd.Flags().GetString("parent")
				if v != "" {
					v, e = resolveBackendID(rows, v)
					if e != nil {
						return backendstore.MutationRequest{}, e
					}
				}
				put("parent_id", v, v == observed.ParentID)
			}
			for _, flag := range []string{"due", "defer"} {
				if !cmd.Flags().Changed(flag) {
					continue
				}
				v, _ := cmd.Flags().GetString(flag)
				if v != "" {
					v, e = dateparse.ParseDate(v)
					if e != nil {
						return backendstore.MutationRequest{}, fmt.Errorf("invalid --%s date: %w", flag, e)
					}
				}
				field, current := "due_date", observed.DueDate
				if flag == "defer" {
					field = "defer_until"
					current = observed.DeferUntil
				}
				same := v == "" && current == nil || current != nil && v == *current
				put(field, v, same)
			}
			if len(patch) == 0 {
				return backendstore.MutationRequest{}, fmt.Errorf("td-backend update has no supported field value")
			}
			if !changed {
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

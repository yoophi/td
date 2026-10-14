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

var errBackendRelationUnchanged = errors.New("dependency already has requested state")

func runBackendRelationship(original, cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	cmd.SetOut(cmd.OutOrStdout())
	if original == depAddCmd || original == depRmCmd || original == depCmd && len(args) == 2 {
		return changeBackendDependencies(original, cmd, args)
	}
	return fmt.Errorf("td-backend does not yet support %s relationship query", cmd.Name())
}

func changeBackendDependencies(original, cmd *cobra.Command, args []string) error {
	adding := original != depRmCmd
	allowed := " json work-dir help refresh "
	if original == depAddCmd {
		allowed += "depends-on "
	}
	var invalid error
	cmd.Flags().Visit(func(flag *pflag.Flag) {
		if !strings.Contains(allowed, " "+flag.Name+" ") {
			invalid = fmt.Errorf("td-backend dep does not support --%s", flag.Name)
		}
	})
	if invalid != nil {
		return invalid
	}
	if len(args) == 0 {
		return fmt.Errorf("issue ID required")
	}
	targets := append([]string{}, args[1:]...)
	if original == depAddCmd {
		flag, _ := cmd.Flags().GetString("depends-on")
		for _, value := range strings.Split(flag, ",") {
			if value = strings.TrimSpace(value); value != "" {
				targets = append(targets, value)
			}
		}
	}
	if len(targets) == 0 {
		return fmt.Errorf("no dependencies specified")
	}
	c, e := openBackendCommand(cmd)
	if e != nil {
		return e
	}
	rows, e := c.List(cmd.Context(), true)
	if e != nil {
		return e
	}
	source, e := resolveBackendID(rows, args[0])
	if e != nil {
		return e
	}
	for index, raw := range targets {
		targets[index], e = resolveBackendID(rows, raw)
		if e != nil {
			return e
		}
	}
	completed := []string{}
	for index, target := range targets {
		var reply struct {
			Dependency models.IssueDependency `json:"dependency"`
			Removed    bool                   `json:"removed"`
		}
		var observed backendstore.Issue
		operation := "dep-add"
		if !adding {
			operation = "dep-rm"
		}
		identity := struct {
			Command any
			Index   int
		}{backendInvocation(cmd, args, operation), index}
		e = c.RunIntent(cmd.Context(), identity, func() (backendstore.MutationRequest, error) {
			detail, e := c.Detail(cmd.Context(), source)
			if e != nil {
				return backendstore.MutationRequest{}, e
			}
			observed = detail.Issue
			edge := models.IssueDependency{}
			for _, dep := range detail.Dependencies {
				if dep.DependsOnID == target {
					edge = dep
					break
				}
			}
			if adding {
				if edge.DependsOnID != "" {
					return backendstore.MutationRequest{}, errBackendRelationUnchanged
				}
				body, e := json.Marshal(map[string]string{"depends_on": target})
				if e != nil {
					return backendstore.MutationRequest{}, e
				}
				return backendstore.MutationRequest{Method: "POST", Resource: "/issues/" + source + "/dependencies", Revision: observed.Revision, Body: body}, nil
			}
			if edge.DependsOnID == "" {
				return backendstore.MutationRequest{}, errBackendRelationUnchanged
			}
			if edge.ID == "" {
				return backendstore.MutationRequest{}, fmt.Errorf("backend dependency has no ID")
			}
			return backendstore.MutationRequest{Method: "DELETE", Resource: "/issues/" + source + "/dependencies/" + edge.ID, Revision: observed.Revision}, nil
		}, &reply, func() error {
			if adding && (reply.Dependency.ID == "" || reply.Dependency.IssueID != source || reply.Dependency.DependsOnID != target) {
				return fmt.Errorf("backend dependency response differs from request")
			}
			if !adding && !reply.Removed {
				return fmt.Errorf("backend did not confirm dependency removal")
			}
			return nil
		})
		noop := errors.Is(e, errBackendRelationUnchanged)
		if noop {
			e = nil
		}
		if e != nil {
			if len(completed) > 0 {
				return fmt.Errorf("dependency change completed for %s; stopped at %s (earlier changes remain; retry the same command): %w", strings.Join(completed, ", "), target, e)
			}
			return e
		}
		observed, e = c.Client.GetIssue(cmd.Context(), c.ProjectID, c.SessionID, source)
		if e != nil {
			return fmt.Errorf("dependency change completed for %s; unable to read updated Issue: %w", target, e)
		}
		completed = append(completed, target)
		if jsonMode(cmd) {
			action := "dependency_added"
			if !adding {
				action = "dependency_removed"
			}
			if e := json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"action": action, "from": source, "to": target, "type": "depends_on", "already_exists": noop, "issue": observed.Issue}); e != nil {
				return e
			}
		} else {
			action := "ADDED"
			if !adding {
				action = "REMOVED"
			}
			if _, e := fmt.Fprintf(cmd.OutOrStdout(), "%s dependency %s -> %s\n", action, source, target); e != nil {
				return e
			}
		}
	}
	return nil
}

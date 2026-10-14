package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/output"
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
	if original == depCmd || original == dependsOnCmd || original == blockedByCmd {
		return queryBackendDependencies(original, cmd, args)
	}
	return fmt.Errorf("td-backend does not yet support %s relationship query", cmd.Name())
}

func queryBackendDependencies(original, cmd *cobra.Command, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("one issue ID required")
	}
	allowed := " json work-dir help refresh "
	if original == depCmd {
		allowed += "blocking "
	}
	if original == blockedByCmd {
		allowed += "direct "
	}
	var invalid error
	cmd.Flags().Visit(func(flag *pflag.Flag) {
		if !strings.Contains(allowed, " "+flag.Name+" ") {
			invalid = fmt.Errorf("td-backend %s does not support --%s", cmd.Name(), flag.Name)
		}
	})
	if invalid != nil {
		return invalid
	}
	c, e := openBackendCommand(cmd)
	if e != nil {
		return e
	}
	var root backendstore.Issue
	var direct, all []string
	reverse := original == blockedByCmd
	if original == depCmd {
		reverse, _ = cmd.Flags().GetBool("blocking")
	}
	e = c.ReadSnapshot(cmd.Context(), func(rows []backendstore.Issue) error {
		id, e := resolveBackendID(rows, args[0])
		if e != nil {
			return e
		}
		graph := map[string][]string{}
		byID := map[string]backendstore.Issue{}
		for _, row := range rows {
			byID[row.ID] = row
			if !reverse && row.ID != id {
				continue
			}
			detail, e := c.Detail(cmd.Context(), row.ID)
			if e != nil {
				return e
			}
			for _, edge := range detail.Dependencies {
				if edge.IssueID != row.ID || !backendstore.ValidIssueID(edge.DependsOnID) {
					return fmt.Errorf("invalid backend dependency graph")
				}
				graph[row.ID] = append(graph[row.ID], edge.DependsOnID)
			}
		}
		root = byID[id]
		if reverse {
			back := map[string][]string{}
			for source, targets := range graph {
				for _, target := range targets {
					back[target] = append(back[target], source)
				}
			}
			graph = back
		}
		direct = append([]string{}, graph[id]...)
		slices.Sort(direct)
		seen := map[string]bool{id: true}
		queue := append([]string{}, direct...)
		all = []string{}
		for len(queue) > 0 {
			next := queue[0]
			queue = queue[1:]
			if seen[next] {
				continue
			}
			seen[next] = true
			all = append(all, next)
			queue = append(queue, graph[next]...)
		}
		slices.Sort(all)
		return nil
	})
	if e != nil {
		return e
	}
	if jsonMode(cmd) {
		var result any
		if original == blockedByCmd {
			result = map[string]any{"issue": root.Issue, "direct": direct, "direct_count": len(direct)}
			isDirect, _ := cmd.Flags().GetBool("direct")
			if !isDirect {
				result.(map[string]any)["all"] = all
				result.(map[string]any)["transitive_count"] = len(all) - len(direct)
			}
		} else if reverse {
			result = map[string]any{"issue": root.Issue, "blocked": direct}
		} else {
			result = map[string]any{"issue": root.Issue, "dependencies": direct}
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}
	if _, e := fmt.Fprintln(cmd.OutOrStdout(), output.IssueOneLiner(&root.Issue)); e != nil {
		return e
	}
	printed := direct
	if original == blockedByCmd {
		isDirect, _ := cmd.Flags().GetBool("direct")
		if !isDirect {
			printed = all
		}
	}
	if len(printed) == 0 {
		_, e = fmt.Fprintln(cmd.OutOrStdout(), "No matching relationships")
		return e
	}
	for _, id := range printed {
		if _, e := fmt.Fprintln(cmd.OutOrStdout(), id); e != nil {
			return e
		}
	}
	return nil
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

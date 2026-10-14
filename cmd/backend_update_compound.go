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

type backendUpdateTarget struct {
	RawID            string            `json:"raw_id"`
	ID               string            `json:"id,omitempty"`
	Error            string            `json:"error,omitempty"`
	Fields           map[string]any    `json:"fields,omitempty"`
	Status           models.Status     `json:"status,omitempty"`
	Comment          string            `json:"comment,omitempty"`
	DependsOn        []string          `json:"depends_on,omitempty"`
	Blocks           []string          `json:"blocks,omitempty"`
	ReplaceDependsOn bool              `json:"replace_depends_on,omitempty"`
	ReplaceBlocks    bool              `json:"replace_blocks,omitempty"`
	InitialDependsOn map[string]string `json:"initial_depends_on,omitempty"`
	InitialBlocks    map[string]string `json:"initial_blocks,omitempty"`
}
type backendUpdatePlan struct {
	Targets []backendUpdateTarget `json:"targets"`
}
type backendUpdateResult struct {
	ID, Outcome, Reason string
	Issue               *models.Issue
}

var errBackendUpdatePartial = errors.New("backend update has failed targets")

const backendUpdateFieldFlags = " title description desc body description-file acceptance acceptance-file type priority points labels sprint parent due defer "

func backendUpdateHasFields(cmd *cobra.Command) bool {
	found := false
	cmd.Flags().Visit(func(f *pflag.Flag) {
		if strings.Contains(backendUpdateFieldFlags, " "+f.Name+" ") {
			found = true
		}
	})
	return found
}

func runBackendCompoundUpdate(cmd *cobra.Command, args []string) error {
	allowed := backendUpdateFieldFlags + " status comment note depends-on blocks append json work-dir help refresh "
	var invalid error
	cmd.Flags().Visit(func(f *pflag.Flag) {
		if !strings.Contains(allowed, " "+f.Name+" ") {
			invalid = fmt.Errorf("td-backend update does not support --%s", f.Name)
		}
	})
	if invalid != nil {
		return invalid
	}
	appendMode, _ := cmd.Flags().GetBool("append")
	if appendMode && len(args) > 1 {
		return fmt.Errorf("td-backend update --append requires one Issue")
	}
	c, err := openBackendCommand(cmd)
	if err != nil {
		return err
	}
	var results []backendUpdateResult
	err = c.RunGroupIntent(cmd.Context(), backendInvocation(cmd, args, "update-compound"), func() (json.RawMessage, error) {
		rows, e := c.Client.ListIssues(cmd.Context(), c.ProjectID, c.SessionID, true)
		if e != nil {
			return nil, e
		}
		plan := backendUpdatePlan{}
		for _, raw := range args {
			target := backendUpdateTarget{RawID: raw, ReplaceDependsOn: cmd.Flags().Changed("depends-on"), ReplaceBlocks: cmd.Flags().Changed("blocks")}
			id, e := resolveBackendID(rows, raw)
			if e != nil {
				target.Error = e.Error()
				plan.Targets = append(plan.Targets, target)
				continue
			}
			target.ID = id
			var observed backendstore.Issue
			for _, row := range rows {
				if row.ID == id {
					observed = row
					break
				}
			}
			if backendUpdateHasFields(cmd) {
				target.Fields, e = backendCompoundFields(cmd, rows, observed, appendMode)
				if e != nil {
					return nil, fmt.Errorf("%s: %w", raw, e)
				}
			}
			if cmd.Flags().Changed("status") {
				rawStatus, _ := cmd.Flags().GetString("status")
				target.Status = models.NormalizeStatus(rawStatus)
				if !models.IsValidStatus(target.Status) {
					return nil, fmt.Errorf("invalid status %q", rawStatus)
				}
				if target.Status == observed.Status {
					target.Status = ""
				}
			}
			target.Comment, _ = cmd.Flags().GetString("comment")
			if target.Comment == "" {
				target.Comment, _ = cmd.Flags().GetString("note")
			}
			if target.ReplaceDependsOn {
				detail, e := c.Detail(cmd.Context(), id)
				if e != nil {
					return nil, e
				}
				target.InitialDependsOn = map[string]string{}
				for _, dep := range detail.Dependencies {
					target.InitialDependsOn[dep.DependsOnID] = dep.ID
				}
				values, _ := cmd.Flags().GetStringArray("depends-on")
				target.DependsOn, e = resolveBackendUpdateIDs(rows, mergeMultiValueFlag(values))
				if e != nil {
					return nil, e
				}
			}
			if target.ReplaceBlocks {
				detail, e := c.Detail(cmd.Context(), id)
				if e != nil {
					return nil, e
				}
				target.InitialBlocks = map[string]string{}
				for _, dep := range detail.BlockedBy {
					target.InitialBlocks[dep.IssueID] = dep.ID
				}
				values, _ := cmd.Flags().GetStringArray("blocks")
				target.Blocks, e = resolveBackendUpdateIDs(rows, mergeMultiValueFlag(values))
				if e != nil {
					return nil, e
				}
			}
			plan.Targets = append(plan.Targets, target)
		}
		return json.Marshal(plan)
	}, func(group string, raw json.RawMessage) error {
		var plan backendUpdatePlan
		if e := json.Unmarshal(raw, &plan); e != nil {
			return e
		}
		results = nil
		for _, target := range plan.Targets {
			if target.Error != "" {
				results = append(results, backendUpdateResult{ID: target.RawID, Outcome: "skipped", Reason: target.Error})
				continue
			}
			e := applyBackendCompoundTarget(cmd, c, group, target)
			if e != nil {
				var api *backendstore.APIError
				if errors.As(e, &api) && api.StatusCode >= 400 && api.StatusCode < 500 {
					results = append(results, backendUpdateResult{ID: target.ID, Outcome: "failed", Reason: e.Error()})
					continue
				}
				return fmt.Errorf("updated %d target(s); stopped at %s (retry the same command): %w", len(results), target.ID, e)
			}
			issue, e := c.Client.GetIssue(cmd.Context(), c.ProjectID, c.SessionID, target.ID)
			if e != nil {
				return fmt.Errorf("updated %s; read after write failed: %w", target.ID, e)
			}
			results = append(results, backendUpdateResult{ID: target.ID, Outcome: "updated", Issue: &issue.Issue})
		}
		for _, result := range results {
			if result.Outcome == "failed" {
				return errBackendUpdatePartial
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, errBackendUpdatePartial) {
		return err
	}
	failed := 0
	for _, result := range results {
		if result.Outcome == "failed" {
			failed++
		}
		if jsonMode(cmd) {
			if e := json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"action": result.Outcome, "id": result.ID, "issue": result.Issue, "reason": result.Reason}); e != nil {
				return e
			}
		} else {
			if _, e := fmt.Fprintf(cmd.OutOrStdout(), "%s %s%s\n", strings.ToUpper(result.Outcome), result.ID, func() string {
				if result.Reason == "" {
					return ""
				}
				return ": " + result.Reason
			}()); e != nil {
				return e
			}
		}
	}
	if failed > 0 {
		return fmt.Errorf("backend update failed for %d target(s)", failed)
	}
	return nil
}

func resolveBackendUpdateIDs(rows []backendstore.Issue, raw []string) ([]string, error) {
	ids := []string{}
	for _, value := range raw {
		id, e := resolveBackendID(rows, value)
		if e != nil {
			return nil, e
		}
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return slices.Compact(ids), nil
}

func backendCompoundFields(cmd *cobra.Command, rows []backendstore.Issue, observed backendstore.Issue, appendMode bool) (map[string]any, error) {
	patch := map[string]any{}
	put := func(name string, value any, same bool) {
		if !same {
			patch[name] = value
		}
	}
	if cmd.Flags().Changed("title") {
		v, _ := cmd.Flags().GetString("title")
		put("title", v, v == observed.Title)
	}
	description, provided, stdinUsed, e := resolveRichTextField(cmd, []string{"description", "desc", "body"}, "description-file", false)
	if e != nil {
		return nil, e
	}
	if provided {
		if appendMode && observed.Description != "" && description != "" {
			description = observed.Description + "\n\n" + description
		}
		put("description", description, description == observed.Description)
	}
	acceptance, provided, _, e := resolveRichTextField(cmd, []string{"acceptance"}, "acceptance-file", stdinUsed)
	if e != nil {
		return nil, e
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
			return nil, fmt.Errorf("invalid type %q", raw)
		}
		put("type", v, v == observed.Type)
	}
	if cmd.Flags().Changed("priority") {
		raw, _ := cmd.Flags().GetString("priority")
		v := models.NormalizePriority(raw)
		if !models.IsValidPriority(v) {
			return nil, fmt.Errorf("invalid priority %q", raw)
		}
		put("priority", v, v == observed.Priority)
	}
	if cmd.Flags().Changed("points") {
		v, _ := cmd.Flags().GetInt("points")
		if v != 0 && !models.IsValidPoints(v) {
			return nil, fmt.Errorf("invalid points %d", v)
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
				return nil, e
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
				return nil, fmt.Errorf("invalid --%s date: %w", flag, e)
			}
		}
		field, current := "due_date", observed.DueDate
		if flag == "defer" {
			field, current = "defer_until", observed.DeferUntil
		}
		same := v == "" && current == nil || current != nil && v == *current
		put(field, v, same)
	}
	return patch, nil
}

func applyBackendCompoundTarget(cmd *cobra.Command, c *backendstore.Connection, group string, target backendUpdateTarget) error {
	step := func(label, method, resource string, body any, verify func(json.RawMessage) error) error {
		var raw json.RawMessage
		err := c.RunGroupStepIntent(cmd.Context(), struct{ Group, Issue, Step string }{group, target.ID, label}, func() (backendstore.MutationRequest, error) {
			observed, e := c.Client.GetIssue(cmd.Context(), c.ProjectID, c.SessionID, target.ID)
			if e != nil {
				return backendstore.MutationRequest{}, e
			}
			payload, e := json.Marshal(body)
			if e != nil {
				return backendstore.MutationRequest{}, e
			}
			return backendstore.MutationRequest{Method: method, Resource: resource, Revision: observed.Revision, Body: payload}, nil
		}, &raw, func() error { return verify(raw) })
		return err
	}
	if len(target.Fields) > 0 {
		if e := step("fields", "PATCH", "/issues/"+target.ID, target.Fields, func(raw json.RawMessage) error { return verifyBackendUpdateIssue(raw, target.ID) }); e != nil {
			return e
		}
	}
	if target.Status != "" {
		if e := step("status", "POST", "/issues/"+target.ID+"/status", map[string]string{"status": string(target.Status)}, func(raw json.RawMessage) error {
			var reply struct {
				Issue backendstore.Issue `json:"issue"`
			}
			if e := json.Unmarshal(raw, &reply); e != nil {
				return e
			}
			if e := backendstore.ValidateIssue(reply.Issue); e != nil {
				return e
			}
			if reply.Issue.ID != target.ID || reply.Issue.Status != target.Status {
				return fmt.Errorf("backend status response differs from request")
			}
			return nil
		}); e != nil {
			return e
		}
	}
	if strings.TrimSpace(target.Comment) != "" {
		if e := step("comment", "POST", "/issues/"+target.ID+"/comments", map[string]string{"text": target.Comment}, func(raw json.RawMessage) error {
			var reply struct {
				Comment backendstore.Comment `json:"comment"`
			}
			if e := json.Unmarshal(raw, &reply); e != nil {
				return e
			}
			if reply.Comment.ID == "" || reply.Comment.IssueID != target.ID || reply.Comment.Text != target.Comment || reply.Comment.SessionID != c.SessionID {
				return fmt.Errorf("backend comment response differs from request")
			}
			return nil
		}); e != nil {
			return e
		}
	}
	if target.ReplaceDependsOn {
		if e := replaceBackendUpdateRelations(cmd, c, group, target.ID, target.InitialDependsOn, target.DependsOn, false); e != nil {
			return e
		}
	}
	if target.ReplaceBlocks {
		if e := replaceBackendUpdateRelations(cmd, c, group, target.ID, target.InitialBlocks, target.Blocks, true); e != nil {
			return e
		}
	}
	return nil
}

func verifyBackendUpdateIssue(raw json.RawMessage, id string) error {
	var reply struct {
		Issue backendstore.Issue `json:"issue"`
	}
	if e := json.Unmarshal(raw, &reply); e != nil {
		return e
	}
	if e := backendstore.ValidateIssue(reply.Issue); e != nil {
		return e
	}
	if reply.Issue.ID != id {
		return fmt.Errorf("backend returned a different Issue")
	}
	return nil
}

func replaceBackendUpdateRelations(cmd *cobra.Command, c *backendstore.Connection, group, root string, existing map[string]string, desired []string, reverse bool) error {
	// Both the desired IDs and initial edges were frozen before the first write.
	// A retry still visits every planned edge with its original idempotency key.
	wanted := map[string]bool{}
	for _, id := range desired {
		wanted[id] = true
	}
	remove := []string{}
	add := []string{}
	for id := range existing {
		if !wanted[id] {
			remove = append(remove, id)
		}
	}
	for id := range wanted {
		if _, ok := existing[id]; !ok {
			add = append(add, id)
		}
	}
	slices.Sort(remove)
	slices.Sort(add)
	for _, id := range remove {
		if e := runBackendUpdateRelationStep(cmd, c, group, root, id, existing[id], reverse, false); e != nil {
			return e
		}
	}
	for _, id := range add {
		if e := runBackendUpdateRelationStep(cmd, c, group, root, id, "", reverse, true); e != nil {
			return e
		}
	}
	return nil
}

func runBackendUpdateRelationStep(cmd *cobra.Command, c *backendstore.Connection, group, root, target, edge string, reverse, adding bool) error {
	source, depends := root, target
	if reverse {
		source, depends = target, root
	}
	action := "remove"
	if adding {
		action = "add"
	}
	direction := "depends"
	if reverse {
		direction = "blocks"
	}
	var reply json.RawMessage
	return c.RunGroupStepIntent(cmd.Context(), struct{ Group, Root, Direction, Action, Source, Target string }{group, root, direction, action, source, depends}, func() (backendstore.MutationRequest, error) {
		detail, e := c.Detail(cmd.Context(), source)
		if e != nil {
			return backendstore.MutationRequest{}, e
		}
		resource := "/issues/" + source + "/dependencies"
		if adding {
			body, _ := json.Marshal(map[string]string{"depends_on": depends})
			return backendstore.MutationRequest{Method: "POST", Resource: resource, Revision: detail.Issue.Revision, Body: body}, nil
		}
		currentEdge := edge
		for _, dep := range detail.Dependencies {
			if dep.DependsOnID == depends {
				currentEdge = dep.ID
				break
			}
		}
		if currentEdge == "" {
			return backendstore.MutationRequest{}, fmt.Errorf("backend dependency has no ID")
		}
		return backendstore.MutationRequest{Method: "DELETE", Resource: resource + "/" + currentEdge, Revision: detail.Issue.Revision}, nil
	}, &reply, func() error {
		if adding {
			var data struct {
				Dependency models.IssueDependency `json:"dependency"`
			}
			if e := json.Unmarshal(reply, &data); e != nil {
				return e
			}
			if data.Dependency.IssueID != source || data.Dependency.DependsOnID != depends {
				return fmt.Errorf("backend dependency response differs from request")
			}
			return nil
		}
		var data struct {
			Removed bool `json:"removed"`
		}
		if e := json.Unmarshal(reply, &data); e != nil {
			return e
		}
		if !data.Removed {
			return fmt.Errorf("backend did not confirm dependency removal")
		}
		return nil
	})
}

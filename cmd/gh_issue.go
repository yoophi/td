package cmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/ghcontext"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/output"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Route only implemented commands. All other SQLite consumers fail explicitly
// in db.Open, so an unsupported command cannot silently mutate local issues.
func init() {
	for _, command := range []*cobra.Command{createCmd, listCmd, showCmd, updateCmd} {
		localRun := command.RunE
		operation := command.Name()
		command.RunE = func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(getBaseDir())
			if err != nil {
				return err
			}
			store, err := config.Store(cfg)
			if err != nil {
				return err
			}
			if store == config.StoreSQLite {
				return localRun(cmd, args)
			}
			cmd.SilenceUsage = true
			return runGitHubIssue(cmd, args, operation, cfg)
		}
	}
}

func gitHubFlags(cmd *cobra.Command, operation string) error {
	allowed := " json work-dir help "
	switch operation {
	case "create":
		allowed += "title type priority points labels label tags tag description desc body notes description-file acceptance acceptance-file minor parent epic depends-on blocks "
	case "update":
		allowed += "title type priority points labels description desc body description-file acceptance acceptance-file append status comment note sprint parent depends-on blocks "
	case "list":
		allowed += "all open status type priority labels id search sort reverse limit long short format no-pager parent epic "
	case "show":
		allowed += "long short format tree children render-markdown "
	}
	var invalid []string
	cmd.Flags().Visit(func(flag *pflag.Flag) {
		if !strings.Contains(allowed, " "+flag.Name+" ") {
			invalid = append(invalid, "--"+flag.Name)
		}
	})
	if len(invalid) > 0 {
		return fmt.Errorf("gh-issue does not support %s for %s; no issue changes were made", strings.Join(invalid, ", "), operation)
	}
	return nil
}

func runGitHubIssue(cmd *cobra.Command, args []string, operation string, cfg *models.Config) error {
	if err := gitHubFlags(cmd, operation); err != nil {
		return err
	}
	if operation == "list" {
		return listGitHubIssues(cmd, args, cfg)
	}
	format, _ := cmd.Flags().GetString("format")
	if format != "" && format != "json" && format != "short" && format != "long" {
		return fmt.Errorf("unsupported output format %q", format)
	}
	relations, err := readGitHubRelationOptions(cmd)
	if err != nil {
		return err
	}
	var change ghstore.Changes
	var created *models.Issue
	var comment string
	if operation == "update" {
		comment, err = gitHubUpdateComment(cmd)
		if err != nil {
			return err
		}
	}
	if operation == "create" {
		created, err = newGitHubIssue(cmd, args)
	} else {
		if len(args) == 0 && operation != "show" {
			return fmt.Errorf("gh-issue requires an issue ID (gh-123, #123 or 123)")
		}
		for _, id := range args {
			if _, err := ghstore.Number(id); err != nil {
				return err
			}
		}
		switch operation {
		case "update":
			change, err = gitHubChanges(cmd, false)

		}
	}
	if err != nil {
		return err
	}
	client, err := ghstore.Open(cmd.Context(), getBaseDir(), cfg.GitHub)
	if err != nil {
		return err
	}
	var scope ghcontext.Scope
	var local *ghcontext.State
	options := ghstore.TransitionOptions{Reason: comment}
	if operation == "create" || comment != "" || change.Status != nil || relations.changed() {
		dir, scopeErr := gitHubContextDirectory()
		if scopeErr != nil {
			return scopeErr
		}
		scope, err = ghcontext.Resolve(cmd.Context(), dir, cfg.GitHub.Repo)
		if err != nil {
			return err
		}
		local, err = scope.Update(cmd.Context(), nil)
		if err != nil {
			return err
		}
		options.SessionID = local.Session.ID
		options.AgentType = local.Session.AgentType
	}
	if change.Status != nil {
		options.Mode, err = resolveReviewPolicyMode(getBaseDir())
		if err != nil {
			return err
		}
		if *change.Status == models.StatusInProgress {
			options.Snapshot, err = gitHubSnapshot(cmd.Context(), scope.Worktree)
			if err != nil {
				return err
			}
		}
	}
	if operation == "create" {
		created.CreatorSession = local.Session.ID
		created.CreatedBranch = scope.Branch
		if relations.changed() {
			// A synthetic graph node validates the requested final graph before
			// creation; it is never written or used as review attribution.
			_, err = prepareGitHubRelations(cmd.Context(), client, &ghstore.Record{Issue: models.Issue{ID: "new-issue"}}, relations)
			if err != nil {
				return err
			}
		}
		record, err := client.Create(cmd.Context(), created)
		if err != nil {
			return err
		}
		if relations.changed() {
			createdID := record.ID
			plan, planErr := prepareGitHubRelations(cmd.Context(), client, record, relations)
			if planErr == nil {
				record, planErr = applyGitHubRelations(cmd.Context(), client, plan, record, local.Session.ID)
			}
			if planErr != nil {
				return fmt.Errorf("issue %s was created, but relationship attachment failed; earlier changes remain, inspect it before retrying creation: %w", createdID, planErr)
			}
		}
		return emitGitHubMutation(cmd, "created", record)
	}
	if operation == "show" {
		if len(args) == 0 {
			args, err = resolveGitHubShowSelection(cmd, cfg, client)
			if err != nil {
				return err
			}
		}
		return showGitHubHierarchy(cmd, args, client, format)
	}

	action := "updated"
	var activity models.Activity
	if comment != "" {
		activity = models.Activity{Kind: "comment", SessionID: local.Session.ID, Message: comment}
	}
	completedIssues := []string{}
	for _, id := range args {
		batchError := func(err error) error {
			if len(completedIssues) == 0 {
				return err
			}
			return fmt.Errorf("update failed for %s; completed issues=%v; earlier changes remain: %w", id, completedIssues, err)
		}
		var record *ghstore.Record
		var relationPlan *gitHubRelationPlan
		if relations.changed() {
			observed, readErr := client.Get(cmd.Context(), id)
			if readErr != nil {
				return batchError(readErr)
			}
			relationPlan, err = prepareGitHubRelations(cmd.Context(), client, observed, relations)
			if err != nil {
				return batchError(err)
			}
			fields := change
			fields.Status = nil
			if fields.HasFields() {
				record, err = client.UpdateObserved(cmd.Context(), observed, fields)
			} else {
				record = observed
			}
		} else if change.Status != nil {
			record, err = client.UpdateWorkflow(cmd.Context(), id, change, options)
		} else if hasGitHubChanges(change) {
			record, err = client.Update(cmd.Context(), id, change)
		} else {
			record, err = client.Get(cmd.Context(), id)
		}
		if err != nil {
			return batchError(fmt.Errorf("%s %s: %w", operation, id, err))
		}

		if relationPlan != nil {
			record, err = applyGitHubRelations(cmd.Context(), client, relationPlan, record, local.Session.ID)
			if err != nil {
				return batchError(err)
			}
		}
		if relationPlan != nil && change.Status != nil {
			record, err = client.UpdateWorkflowObserved(cmd.Context(), record, ghstore.Changes{Status: change.Status}, options)
			if err != nil {
				return batchError(fmt.Errorf("%s field/relation changes were saved, but requested status transition failed; earlier changes remain: %w", id, err))
			}
		}
		if change.Status != nil {
			_, err = scope.Update(cmd.Context(), func(current *ghcontext.State) error {
				if current.Session.ID != local.Session.ID {
					return fmt.Errorf("local session changed during update")
				}
				if record.Status == models.StatusInProgress {
					current.Focus = record.ID
				} else if record.Status != models.StatusBlocked && current.Focus == record.ID {
					current.Focus = ""
				}
				return nil
			})
			if err != nil {
				return batchError(fmt.Errorf("%s was updated, but local focus update failed: %w", record.ID, err))
			}
		}

		if comment != "" {
			if _, err := client.AppendActivity(cmd.Context(), record.ID, activity); err != nil {
				if hasGitHubChanges(change) || relations.changed() {
					return batchError(fmt.Errorf("issue %s was updated, but its comment failed; do not repeat the entire update: %w", record.ID, err))
				}
				return batchError(fmt.Errorf("comment on %s: %w", record.ID, err))
			}
		}
		if err := emitGitHubMutation(cmd, action, record); err != nil {
			return batchError(err)
		}
		completedIssues = append(completedIssues, record.ID)
	}
	return nil
}

func emitGitHubMutation(cmd *cobra.Command, action string, record *ghstore.Record) error {
	if jsonMode(cmd) {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{
			"id": record.ID, "status": record.Status, "action": action, "issue": record,
		})
	}
	cmd.Printf("%s %s: %s\n%s\n", strings.ToUpper(action), record.ID, output.SanitizeIssueText(record.Title), record.URL)
	return nil
}

func newGitHubIssue(cmd *cobra.Command, args []string) (*models.Issue, error) {
	issue := &models.Issue{Type: models.TypeTask, Priority: models.PriorityP2, Status: models.StatusOpen}
	issue.Minor, _ = cmd.Flags().GetBool("minor")
	// Preserve the existing `td new task "Title"` shorthand.
	if len(args) == 2 && models.IsValidType(models.NormalizeType(args[0])) {
		issue.Type = models.NormalizeType(args[0])
		args = args[1:]
	}
	if len(args) > 1 {
		return nil, fmt.Errorf("expected one title; quote titles containing spaces")
	}
	issue.Title, _ = cmd.Flags().GetString("title")
	if len(args) == 1 {
		issue.Title = args[0]
	}
	change, err := gitHubChanges(cmd, true)
	if err != nil {
		return nil, err
	}
	if change.Type != nil {
		issue.Type = *change.Type
	} else {
		if extracted, title := parseTypeFromTitle(issue.Title); extracted != "" {
			issue.Type, issue.Title = extracted, title
		}
	}
	min, max, err := config.GetTitleLengthLimits(getBaseDir())
	if err != nil {
		return nil, err
	}
	warning, err := validateTitle(issue.Title, min, max)
	if err != nil {
		return nil, err
	}
	if warning != "" && !jsonMode(cmd) {
		cmd.PrintErrln(warning)
	}
	if change.Description != nil {
		issue.Description = *change.Description
	}
	if change.Acceptance != nil {
		issue.Acceptance = *change.Acceptance
	}
	if change.Priority != nil {
		issue.Priority = *change.Priority
	}
	if change.Points != nil {
		issue.Points = *change.Points
	}
	if change.Labels != nil {
		issue.Labels = *change.Labels
	}
	if change.ParentID != nil {
		issue.ParentID = *change.ParentID
	}
	return issue, nil
}

func gitHubChanges(cmd *cobra.Command, create bool) (ghstore.Changes, error) {
	change := ghstore.Changes{}
	parentFlags := []string{"parent"}
	if create {
		parentFlags = append(parentFlags, "epic")
	}
	for _, flag := range parentFlags {
		if !cmd.Flags().Changed(flag) {
			continue
		}
		value, _ := cmd.Flags().GetString(flag)
		if value != "" {
			number, err := ghstore.Number(value)
			if err != nil {
				return change, err
			}
			value = fmt.Sprintf("gh-%d", number)
		}
		if change.ParentID != nil && *change.ParentID != value {
			return change, fmt.Errorf("--parent and --epic specify different parents")
		}
		change.ParentID = &value
	}
	if !create && cmd.Flags().Changed("sprint") {
		value, _ := cmd.Flags().GetString("sprint")
		change.Sprint = &value
	}
	if cmd.Flags().Changed("title") {
		value, _ := cmd.Flags().GetString("title")
		if !create && strings.TrimSpace(value) == "" {
			return change, fmt.Errorf("title cannot be empty")
		}
		change.Title = &value
	}
	inline := []string{"description", "desc", "body"}
	if create {
		inline = append(inline, "notes")
	}
	text, present, stdinUsed, err := resolveRichTextField(cmd, inline, "description-file", false)
	if err != nil {
		return change, err
	}
	for _, name := range inline {
		present = present || cmd.Flags().Changed(name)
	}
	if present {
		change.Description = &text
	}
	acceptance, present, _, err := resolveRichTextField(cmd, []string{"acceptance"}, "acceptance-file", stdinUsed)
	if err != nil {
		return change, err
	}
	if present || cmd.Flags().Changed("acceptance") {
		change.Acceptance = &acceptance
	}
	if cmd.Flags().Changed("type") {
		value, _ := cmd.Flags().GetString("type")
		normalized := models.NormalizeType(value)
		if !models.IsValidType(normalized) {
			return change, fmt.Errorf("invalid type %q", value)
		}
		change.Type = &normalized
	}
	if cmd.Flags().Changed("priority") {
		value, _ := cmd.Flags().GetString("priority")
		normalized := models.NormalizePriority(value)
		if !models.IsValidPriority(normalized) {
			return change, fmt.Errorf("invalid priority %q", value)
		}
		change.Priority = &normalized
	}
	if cmd.Flags().Changed("points") {
		value, _ := cmd.Flags().GetInt("points")
		if value != 0 && !models.IsValidPoints(value) {
			return change, fmt.Errorf("invalid points %d", value)
		}
		change.Points = &value
	}
	labelFlags := []string{"labels"}
	if create {
		labelFlags = append(labelFlags, "label", "tags", "tag")
	}
	for _, flag := range labelFlags {
		if cmd.Flags().Changed(flag) {
			if change.Labels != nil {
				return change, fmt.Errorf("specify only one labels flag spelling")
			}
			values, _ := cmd.Flags().GetStringArray(flag)
			labels := mergeMultiValueFlag(values)
			change.Labels = &labels
		}
	}
	if !create {
		change.Append, _ = cmd.Flags().GetBool("append")
		if cmd.Flags().Changed("status") {
			value, _ := cmd.Flags().GetString("status")
			status := models.NormalizeStatus(value)
			if !models.IsValidStatus(status) {
				return change, fmt.Errorf("invalid status %q (valid: open, in_progress, in_review, blocked, closed)", value)
			}
			change.Status = &status
		}
		if !hasGitHubChanges(change) && !cmd.Flags().Changed("comment") && !cmd.Flags().Changed("note") && !cmd.Flags().Changed("depends-on") && !cmd.Flags().Changed("blocks") {
			return change, fmt.Errorf("no issue changes specified")
		}
	}
	return change, nil
}

func hasGitHubChanges(change ghstore.Changes) bool {
	return change.Status != nil || change.HasFields()
}

func gitHubUpdateComment(cmd *cobra.Command) (string, error) {
	var text string
	for _, flag := range []string{"comment", "note"} {
		if !cmd.Flags().Changed(flag) {
			continue
		}
		if text != "" {
			return "", fmt.Errorf("specify only one of --comment and --note")
		}
		text, _ = cmd.Flags().GetString(flag)
		if strings.TrimSpace(text) == "" {
			return "", fmt.Errorf("--%s must not be empty", flag)
		}
		if strings.Contains(text, "<!-- td:activity:") {
			return "", fmt.Errorf("comment contains reserved td metadata marker")
		}
	}
	return text, nil
}

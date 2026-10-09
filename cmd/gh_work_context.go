package cmd

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"

	"github.com/marcus/td/internal/ghcontext"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/output"
	"github.com/marcus/td/internal/serve"
	"github.com/spf13/cobra"
)

func runGitHubWorkContext(original, cmd *cobra.Command, args []string, cfg *models.Config) error {
	if original == resumeCmd && len(args) != 1 {
		return fmt.Errorf("resume requires one issue ID")
	}
	if original != resumeCmd && len(args) != 0 {
		return fmt.Errorf("%s does not accept positional arguments", original.Name())
	}
	client, err := ghstore.Open(cmd.Context(), getBaseDir(), cfg.GitHub)
	if err != nil {
		return err
	}
	dir, err := gitHubContextDirectory()
	if err != nil {
		return err
	}
	scope, err := ghcontext.Resolve(cmd.Context(), dir, cfg.GitHub.Repo)
	if err != nil {
		return err
	}
	state, err := scope.Update(cmd.Context(), nil)
	if err != nil {
		return err
	}
	candidate := *state
	candidate.History = slices.Clone(state.History)
	rotate, _ := cmd.Flags().GetBool("new-session")
	if rotate {
		scope.NewSession(&candidate)
	}
	if original == resumeCmd {
		target, err := client.Get(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		candidate.Focus = target.ID
	}
	mode, err := resolveReviewPolicyMode(getBaseDir())
	if err != nil {
		return err
	}
	var focus *string
	if candidate.Focus != "" {
		focus = &candidate.Focus
	}
	snapshot, err := serve.ReadGitHubContext(cmd.Context(), client, candidate.Session.ID, mode, focus)
	if err != nil {
		return err
	}
	if original == resumeCmd && snapshot.Data.FocusedIssue == nil {
		return fmt.Errorf("resume target %s is missing from the current visible listing; focus was not changed", candidate.Focus)
	}
	if rotate || original == resumeCmd {
		_, err = scope.Update(cmd.Context(), func(current *ghcontext.State) error {
			if !reflect.DeepEqual(current, state) {
				return fmt.Errorf("local session or focus changed while context was being read; no context changes saved")
			}
			*current = candidate
			return nil
		})
		if err != nil {
			return err
		}
	}
	payload := githubWorkContextPayload(&candidate, snapshot)
	for _, record := range snapshot.Records {
		if record.ID == candidate.Focus && record.DeletedAt == nil {
			payload["focused_record"] = record
		}
	}
	payload["action"] = original.Name()
	payload["review_policy_mode"] = mode
	if original == statusCmd {
		payload["in_review"] = map[string]any{"reviewable_by_you": jsonList(snapshot.Data.TaskList.Reviewable), "ready_to_close": jsonList(snapshot.Data.TaskList.ReadyToClose), "implemented_by_you": jsonList(snapshot.Data.TaskList.PendingReview), "pending_other": jsonList(snapshot.Data.TaskList.PendingOther), "total": len(snapshot.Data.TaskList.Reviewable) + len(snapshot.Data.TaskList.ReadyToClose) + len(snapshot.Data.TaskList.PendingReview) + len(snapshot.Data.TaskList.PendingOther)}
	}
	if original == resumeCmd {
		payload["action"] = "focused"
		payload["id"] = candidate.Focus
		payload["issue"] = snapshot.Data.FocusedIssue
		payload["status"] = snapshot.Data.FocusedIssue.Status
	}
	if jsonMode(cmd) {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(payload)
	}
	cmd.Printf("SESSION: %s (device-local, branch %s)\n", output.SanitizeIssueText(candidate.Session.Display()), output.SanitizeIssueText(candidate.Session.Branch))
	if candidate.Session.PreviousSessionID != "" {
		cmd.Printf("PREVIOUS SESSION: %s\n", candidate.Session.PreviousSessionID)
	}
	if snapshot.Data.FocusedIssue != nil {
		i := snapshot.Data.FocusedIssue
		cmd.Printf("FOCUSED %s [%s] %s\n", i.ID, i.Status, output.SanitizeIssueText(i.Title))
	} else if candidate.Focus != "" {
		cmd.Printf("SAVED FOCUS %s is missing or deleted; selection preserved.\n", output.SanitizeIssueText(candidate.Focus))
	}
	touched := payload["issues_touched"].([]string)
	if original == whoamiCmd {
		cmd.Printf("STARTED: %s\n", candidate.Session.StartedAt.UTC().Format("2006-01-02T15:04:05Z07:00"))
		cmd.Printf("ISSUES TOUCHED: %v\n", touched)
		return nil
	}
	if original == resumeCmd {
		cmd.Print(output.FormatIssueLong(output.SanitizedForDisplay(snapshot.Data.FocusedIssue), nil, nil))
		for _, record := range snapshot.Records {
			if record.ID == candidate.Focus && record.Details != nil {
				for _, event := range record.Details.Transitions {
					cmd.Printf("WORKFLOW %s %s -> %s (%s): %s\n", output.SanitizeIssueText(event.Action), event.From, event.To, output.SanitizeIssueText(event.SessionID), output.SanitizeIssueText(event.Reason))
				}
				for _, review := range record.Details.Reviews {
					cmd.Printf("REVIEW %s (%s): %s\n", review.Decision, output.SanitizeIssueText(review.ReviewerSession), output.SanitizeIssueText(review.Summary))
				}
			}
		}
		for _, a := range snapshot.Activities[candidate.Focus] {
			cmd.Printf("%s [%s] %s\n", a.CreatedAt.Format("2006-01-02T15:04:05Z07:00"), a.Kind, output.SanitizeIssueText(a.Message))
			if a.Kind == "handoff" {
				cmd.Printf("  done=%v remaining=%v decisions=%v uncertain=%v\n", sanitizeContextItems(a.Done), sanitizeContextItems(a.Remaining), sanitizeContextItems(a.Decisions), sanitizeContextItems(a.Uncertain))
			}
		}
		return nil
	}
	compact, _ := cmd.Flags().GetBool("compact")
	progress := snapshot.Data.InProgress
	if original == usageCmd {
		progress = payload["in_progress"].([]models.Issue)
	}
	for _, section := range []struct {
		name   string
		issues []models.Issue
	}{{"AWAITING YOUR REVIEW", snapshot.Data.TaskList.Reviewable}, {"READY TO CLOSE", snapshot.Data.TaskList.ReadyToClose}, {"PENDING REVIEW", snapshot.Data.TaskList.PendingReview}, {"PENDING OTHER / STALE REVIEW", snapshot.Data.TaskList.PendingOther}, {"IN PROGRESS", progress}, {"NEEDS REWORK", snapshot.Data.TaskList.NeedsRework}, {"BLOCKED", snapshot.Data.TaskList.Blocked}, {"READY TO START", snapshot.Data.TaskList.Ready}} {
		if len(section.issues) == 0 {
			continue
		}
		cmd.Printf("%s (%d):\n", section.name, len(section.issues))
		limit := len(section.issues)
		if compact && limit > 5 {
			limit = 5
		}
		for _, i := range section.issues[:limit] {
			cmd.Printf("  %s [%s] %s: %s\n", i.Priority, i.Status, i.ID, output.SanitizeIssueText(i.Title))
		}
	}
	for _, a := range snapshot.Activities[candidate.Focus] {
		if a.Kind == "handoff" {
			cmd.Printf("HANDOFF %s (%s): done=%v remaining=%v decisions=%v uncertain=%v\n", candidate.Focus, a.CreatedAt.Format("2006-01-02T15:04:05Z07:00"), sanitizeContextItems(a.Done), sanitizeContextItems(a.Remaining), sanitizeContextItems(a.Decisions), sanitizeContextItems(a.Uncertain))
		}
	}
	cmd.Println("Work-session context is not yet supported (#15); this is not an empty work-session result.")
	quiet, _ := cmd.Flags().GetBool("quiet")
	if !quiet {
		cmd.Println("Issues/history are shared; identity/focus are device-local. Review queues are observations; trusted self-review needs acknowledgement. Use handoff, review, then approve.")
	}
	return nil
}

func sanitizeContextItems(items []string) []string {
	result := make([]string, 0, len(items))
	for _, item := range items {
		result = append(result, output.SanitizeIssueText(item))
	}
	return result
}

func githubWorkContextPayload(state *ghcontext.State, snapshot *serve.GitHubContextSnapshot) map[string]any {
	d := snapshot.Data
	touched := []string{}
	history := map[string][]models.Activity{}
	handoffs := map[string][]models.Activity{}
	workflowHistory := map[string][]ghstore.TransitionRecord{}
	reviewHistory := map[string][]models.IssueReview{}
	myProgress := []models.Issue{}
	visible := []ghstore.Record{}
	for _, record := range snapshot.Records {
		if record.DeletedAt == nil && record.Status != models.StatusClosed {
			visible = append(visible, record)
		}
		mine := record.CreatorSession == state.Session.ID || record.ImplementerSession == state.Session.ID || record.ReviewerSession == state.Session.ID || record.ReviewRequestedBySession == state.Session.ID || record.ClosedBySession == state.Session.ID
		if record.Details != nil {
			for _, event := range record.Details.Transitions {
				if event.SessionID == state.Session.ID {
					mine = true
				}
			}
		}
		activities := snapshot.Activities[record.ID]
		for _, a := range activities {
			if a.SessionID == state.Session.ID {
				mine = true
			}
			if a.Kind == "handoff" {
				handoffs[record.ID] = append(handoffs[record.ID], a)
			}
		}
		if record.Details != nil {
			for _, review := range record.Details.Reviews {
				if review.ReviewerSession == state.Session.ID || review.RequestedBySession == state.Session.ID {
					mine = true
				}
			}
		}
		if mine {
			if record.Details != nil {
				workflowHistory[record.ID] = jsonList(record.Details.Transitions)
				reviewHistory[record.ID] = jsonList(record.Details.Reviews)
			}
			touched = append(touched, record.ID)
			history[record.ID] = jsonList(activities)
		}
		if record.DeletedAt == nil && record.Status == models.StatusInProgress && record.ImplementerSession == state.Session.ID {
			myProgress = append(myProgress, record.Issue)
		}
	}
	slices.Sort(touched)
	slices.SortFunc(visible, func(a, b ghstore.Record) int {
		if a.Priority != b.Priority {
			if a.Priority < b.Priority {
				return -1
			}
			return 1
		}
		if a.Number < b.Number {
			return -1
		}
		if a.Number > b.Number {
			return 1
		}
		return 0
	})
	return map[string]any{"session": state.Session.ID, "name": state.Session.Name, "branch": state.Session.Branch, "agent": state.Session.AgentType, "started": state.Session.StartedAt, "previous_session": state.Session.PreviousSessionID, "storage": "device-local", "saved_focus": state.Focus, "focused": d.FocusedIssue, "focus": d.FocusedIssue, "focus_missing": state.Focus != "" && d.FocusedIssue == nil, "issues_touched": touched, "issues": visible, "history": history, "workflow_history": workflowHistory, "review_history": reviewHistory, "handoffs": handoffs, "local_session_history": jsonList(state.History), "in_progress": myProgress, "all_in_progress": jsonList(d.InProgress), "reviewable": jsonList(d.TaskList.Reviewable), "ready_to_close": jsonList(d.TaskList.ReadyToClose), "pending_review": jsonList(d.TaskList.PendingReview), "pending_other": jsonList(d.TaskList.PendingOther), "needs_rework": jsonList(d.TaskList.NeedsRework), "blocked": jsonList(d.TaskList.Blocked), "ready": jsonList(d.TaskList.Ready), "ready_to_start": jsonList(d.TaskList.Ready), "review_workflows_supported": true, "work_sessions_supported": false, "work_session_context_error": "work-session context is not implemented; tracked by #15"}
}

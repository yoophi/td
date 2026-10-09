package cmd

import (
	"context"
	"crypto/rand"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/marcus/td/internal/ghcontext"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/output"
	"github.com/spf13/cobra"
)

type githubWorkSessionReader interface {
	GetIncludingDeleted(context.Context, string) (*ghstore.Record, error)
	ListActivityIncludingDeleted(context.Context, string) ([]models.Activity, error)
}

// Shared activity is always read from GitHub, including previously untagged
// issues. Only entries made without any issue target remain device-local.
func readGitHubWorkSessionActivity(ctx context.Context, client githubWorkSessionReader, ws *ghcontext.WorkSession) ([]models.Activity, error) {
	ids := slices.Clone(ws.HistoryIssues)
	for _, id := range ws.Issues {
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	activities := slices.Clone(ws.LocalActivities)
	for _, id := range ids {
		rows, err := client.ListActivityIncludingDeleted(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("read work-session history on %s: %w", id, err)
		}
		for _, a := range rows {
			if a.WorkSessionID == ws.ID {
				activities = append(activities, a)
			}
		}
	}
	slices.SortStableFunc(activities, func(a, b models.Activity) int { return a.CreatedAt.Compare(b.CreatedAt) })
	return jsonList(activities), nil
}

func showGitHubWorkSession(cmd *cobra.Command, client githubWorkSessionReader, ws *ghcontext.WorkSession, past bool) error {
	issues := make([]ghstore.Record, 0, len(ws.Issues))
	for _, id := range ws.Issues {
		r, err := client.GetIncludingDeleted(cmd.Context(), id)
		if err != nil {
			return fmt.Errorf("read tagged issue %s: %w", id, err)
		}
		issues = append(issues, *r)
	}
	activities, err := readGitHubWorkSessionActivity(cmd.Context(), client, ws)
	if err != nil {
		return err
	}
	if jsonMode(cmd) {
		return githubWSJSON(cmd, map[string]any{"work_session": ws.WorkSession, "issues": jsonList(ws.Issues), "issue_details": issues, "activities": activities, "storage": "device-local", "shared_activity_storage": "github-issue-comments"})
	}
	cmd.Printf("WORK SESSION: %s %q\nStarted: %s\n", ws.ID, output.SanitizeIssueText(ws.Name), output.FormatTimeAgo(ws.StartedAt))
	if ws.EndedAt != nil {
		cmd.Printf("Ended: %s (duration %s)\n", output.FormatTimeAgo(*ws.EndedAt), ws.EndedAt.Sub(ws.StartedAt).Round(time.Second))
	}
	if ws.StartSHA != "" {
		if ws.EndSHA == "" {
			cmd.Printf("Git start: %s\n", output.ShortSHA(ws.StartSHA))
		} else {
			cmd.Printf("Git: %s → %s\n", output.ShortSHA(ws.StartSHA), output.ShortSHA(ws.EndSHA))
		}
	}
	for _, i := range issues {
		deleted := ""
		if i.DeletedAt != nil {
			deleted = " (deleted)"
		}
		cmd.Printf("  %s  %s  %s  %s%s\n", i.ID, output.SanitizeIssueText(i.Title), i.Status, i.Priority, deleted)
	}
	full, _ := cmd.Flags().GetBool("full")
	display := activities
	if !past && len(display) > 5 {
		display = display[len(display)-5:]
	}
	for _, a := range display {
		if past && !full && a.Kind != "handoff" {
			continue
		}
		cmd.Printf("  [%s] %s %s [%s] %s\n", a.CreatedAt.Format(time.RFC3339), a.Kind, a.IssueID, a.LogType, output.SanitizeIssueText(a.Message))
		if a.Kind == "handoff" {
			cmd.Printf("    done=%v remaining=%v decisions=%v uncertain=%v\n", sanitizeContextItems(a.Done), sanitizeContextItems(a.Remaining), sanitizeContextItems(a.Decisions), sanitizeContextItems(a.Uncertain))
		}
	}
	return nil
}

func writeGitHubWorkSessionActivity(original, cmd *cobra.Command, args []string, client *ghstore.Client, scope ghcontext.Scope, state *ghcontext.State) error {
	ws, err := state.CurrentWorkSession()
	if err != nil {
		return err
	}
	activity := models.Activity{SessionID: state.Session.ID, WorkSessionID: ws.ID, OperationID: "td-op-" + rand.Text()}
	targets := slices.Clone(ws.Issues)
	if original == wsLogCmd {
		activity.Kind = "log"
		activity.Message = args[0]
		activity.LogType = models.LogTypeProgress
		if strings.TrimSpace(activity.Message) == "" {
			return fmt.Errorf("work-session log message must not be empty")
		}
		selected := 0
		for _, typ := range []string{"blocker", "decision", "hypothesis", "tried", "result"} {
			if set, _ := cmd.Flags().GetBool(typ); set {
				activity.LogType = models.LogType(typ)
				selected++
			}
		}
		if selected > 1 {
			return fmt.Errorf("choose only one work-session log type")
		}
		if only, _ := cmd.Flags().GetString("only"); only != "" {
			targets = []string{only}
		}
	} else {
		activity.Kind = "handoff"
		if err := readGitHubHandoff(cmd, &activity); err != nil {
			return err
		}
		if len(activity.Done)+len(activity.Remaining)+len(activity.Decisions)+len(activity.Uncertain) == 0 {
			logs, err := readGitHubWorkSessionActivity(cmd.Context(), client, ws)
			if err != nil {
				return err
			}
			seen := map[string]bool{}
			for _, a := range logs {
				if a.Kind != "log" {
					continue
				}
				key := a.OperationID
				if key == "" {
					key = a.IssueID + "/" + a.ID
				}
				if seen[key] {
					continue
				}
				seen[key] = true
				switch a.LogType {
				case models.LogTypeProgress, models.LogTypeResult:
					activity.Done = append(activity.Done, a.Message)
				case models.LogTypeDecision:
					activity.Decisions = append(activity.Decisions, a.Message)
				case models.LogTypeBlocker:
					activity.Uncertain = append(activity.Uncertain, a.Message)
				}
			}
		}
		if len(activity.Done)+len(activity.Remaining)+len(activity.Decisions)+len(activity.Uncertain) == 0 {
			return fmt.Errorf("handoff has no content; provide flags or record work-session logs first")
		}
		snapshot, err := gitHubSnapshot(cmd.Context(), scope.Worktree)
		if err != nil {
			return err
		}
		snapshot.Event = "handoff"
		activity.Snapshot = snapshot
	}
	if activity.Kind == "handoff" {
		for _, items := range [][]string{activity.Done, activity.Remaining, activity.Decisions, activity.Uncertain} {
			for _, item := range items {
				if strings.TrimSpace(item) == "" {
					return fmt.Errorf("handoff items must not be empty")
				}
			}
		}
	}
	// Resolve every target before writing the first comment.
	records := make([]*ghstore.Record, 0, len(targets))
	for _, id := range targets {
		r, err := client.Get(cmd.Context(), id)
		if err != nil {
			return err
		}
		if activity.Kind == "handoff" && len(activity.Done)+len(filterGitHubWSRemaining(activity.Remaining, r.ID))+len(activity.Decisions)+len(activity.Uncertain) == 0 {
			return fmt.Errorf("handoff has no content for %s after issue-specific filtering; no comments written", r.ID)
		}
		records = append(records, r)
	}
	review, _ := cmd.Flags().GetBool("review")
	mode, err := resolveReviewPolicyMode(getBaseDir())
	if err != nil {
		return err
	}
	if review && len(records) == 0 {
		return fmt.Errorf("handoff --review requires at least one tagged issue")
	}
	checkActive := func(current *ghcontext.State) (*ghcontext.WorkSession, error) {
		active, err := current.CurrentWorkSession()
		if err != nil {
			return nil, err
		}
		if current.Session.ID != state.Session.ID || active.ID != ws.ID || !slices.Equal(active.Issues, ws.Issues) {
			return nil, fmt.Errorf("active work-session context or tags changed; reread before retrying")
		}
		return active, nil
	}
	completed := []string{}
	if len(records) == 0 {
		_, err = scope.Update(cmd.Context(), func(current *ghcontext.State) error {
			active, err := checkActive(current)
			if err != nil {
				return err
			}
			now := time.Now().UTC()
			activity.CreatedAt = now
			activity.UpdatedAt = now
			active.LocalActivities = append(active.LocalActivities, activity)
			return nil
		})
		if err != nil {
			return err
		}
	} else {
		for _, record := range records {
			entry := activity
			if entry.Kind == "handoff" {
				entry.Remaining = filterGitHubWSRemaining(entry.Remaining, record.ID)
			}
			_, err = scope.Update(cmd.Context(), func(current *ghcontext.State) error {
				active, err := checkActive(current)
				if err != nil {
					return err
				}
				if _, err := client.AppendActivity(cmd.Context(), record.ID, entry); err != nil {
					return err
				}
				if !slices.Contains(active.HistoryIssues, record.ID) {
					active.HistoryIssues = append(active.HistoryIssues, record.ID)
				}
				return nil
			})
			if err != nil {
				return fmt.Errorf("ws %s failed on %s; completed comments=%v, operation=%s (inspect remote history before retrying; work session remains active): %w", original.Name(), record.ID, completed, activity.OperationID, err)
			}
			completed = append(completed, record.ID)
		}
	}
	reviewed := []string{}
	if review {
		for _, record := range records {
			requested := false
			_, err = scope.Update(cmd.Context(), func(current *ghcontext.State) error {
				if _, err := checkActive(current); err != nil {
					return err
				}
				observed, err := client.Get(cmd.Context(), record.ID)
				if err != nil {
					return err
				}
				if observed.Status != models.StatusInProgress {
					return nil
				}
				requested = true
				_, _, err = client.TransitionObserved(cmd.Context(), observed, "review", ghstore.TransitionOptions{SessionID: state.Session.ID, AgentType: state.Session.AgentType, Mode: mode, Reason: "Submitted for review via ws handoff --review", Snapshot: activity.Snapshot})
				return err
			})
			if err != nil {
				return fmt.Errorf("handoffs recorded on %v; review failed on %s after %v (work session remains active, no automatic retry): %w", completed, record.ID, reviewed, err)
			}
			if requested {
				reviewed = append(reviewed, record.ID)
			}
		}
	}
	continuing, _ := cmd.Flags().GetBool("continue")
	if original == wsHandoffCmd && !continuing {
		_, err = scope.Update(cmd.Context(), func(current *ghcontext.State) error {
			if _, err := checkActive(current); err != nil {
				return err
			}
			_, err := current.EndWorkSession(activity.Snapshot.CommitSHA)
			return err
		})
		if err != nil {
			return fmt.Errorf("handoffs recorded but work-session end failed: %w", err)
		}
	}
	action := "logged_work_session"
	if original == wsHandoffCmd {
		action = "handed_off_work_session"
	}
	if jsonMode(cmd) {
		return githubWSResult(cmd, action, map[string]any{"work_session": ws.ID, "issues": completed, "operation_id": activity.OperationID, "device_local_only": len(records) == 0, "ended": original == wsHandoffCmd && !continuing, "review_requested": review, "review_requests": reviewed})
	}
	cmd.Printf("%s %s → %v (operation %s)\n", strings.ToUpper(original.Name()), ws.ID, completed, activity.OperationID)
	if len(records) == 0 {
		cmd.Println("No issues tagged; activity saved only in this device-local bundle.")
	}
	return nil
}

func filterGitHubWSRemaining(items []string, issueID string) []string {
	result := []string{}
	for _, item := range items {
		if strings.Contains(item, "("+issueID+")") {
			result = append(result, strings.TrimSpace(strings.ReplaceAll(item, "("+issueID+")", "")))
		} else if !strings.Contains(item, "(gh-") && !strings.Contains(item, "(td-") {
			result = append(result, item)
		}
	}
	return result
}

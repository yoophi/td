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

var errBackendTransitionUnchanged = errors.New("issue already has requested workflow state")

func runBackendTransition(cmd *cobra.Command, args []string, action string) error {
	cmd.SilenceUsage = true
	if len(args) == 0 {
		return fmt.Errorf("%s requires at least one issue ID", action)
	}
	allowed := " json work-dir help refresh reason "
	if action == "start" {
		allowed += "force "
	}
	if action == "review" || action == "approve" || action == "close" || action == "reject" {
		allowed += "message comment note notes "
	}
	if action == "approve" {
		allowed += "record-only self-review reviewed-by decision "
	}
	if action == "close" {
		allowed += "admin self-close-exception "
	}
	var invalid error
	cmd.Flags().Visit(func(f *pflag.Flag) {
		if !strings.Contains(allowed, " "+f.Name+" ") {
			invalid = fmt.Errorf("td-backend does not support --%s for %s", f.Name, action)
		}
	})
	if invalid != nil {
		return invalid
	}
	reason := approvalReason(cmd)
	if cmd.Flags().Changed("reason") && strings.TrimSpace(reason) == "" {
		return fmt.Errorf("--reason requires a nonblank value")
	}
	force := false
	if action == "start" {
		force, _ = cmd.Flags().GetBool("force")
	}
	recordOnly, _ := cmd.Flags().GetBool("record-only")
	selfReview, _ := cmd.Flags().GetBool("self-review")
	reviewedBy, _ := cmd.Flags().GetString("reviewed-by")
	decision, _ := cmd.Flags().GetString("decision")
	if action == "approve" {
		if strings.TrimSpace(reviewedBy) == "" && cmd.Flags().Changed("reviewed-by") {
			return fmt.Errorf("--reviewed-by requires a nonblank name")
		}
		if selfReview && reviewedBy != "" {
			return fmt.Errorf("--self-review and --reviewed-by are mutually exclusive")
		}
		if decision != "" && decision != "approved" && decision != "changes_requested" {
			return fmt.Errorf("--decision must be approved or changes_requested")
		}
		if decision == "changes_requested" && !recordOnly {
			return fmt.Errorf("--decision changes_requested requires --record-only")
		}
		if recordOnly && strings.TrimSpace(reason) == "" {
			return fmt.Errorf("--record-only requires --reason")
		}
		if recordOnly && decision == "" {
			decision = "approved"
		}
	}
	c, e := openBackendCommand(cmd)
	if e != nil {
		return e
	}
	target, label := models.StatusInProgress, "STARTED"
	switch action {
	case "unstart":
		target, label = models.StatusOpen, "UNSTARTED"
	case "block":
		target, label = models.StatusBlocked, "BLOCKED"
	case "unblock":
		target, label = models.StatusOpen, "UNBLOCKED"
	case "reopen":
		target, label = models.StatusOpen, "REOPENED"
	case "review":
		target, label = models.StatusInReview, "REVIEWED"
	case "approve":
		target, label = models.StatusClosed, "APPROVED"
		if recordOnly {
			target, label = models.StatusInReview, "REVIEW RECORDED"
		}
	case "close":
		target, label = models.StatusClosed, "CLOSED"
	case "reject":
		target, label = models.StatusOpen, "REJECTED"
	}
	completed := []string{}
	for index, rawID := range args {
		var reply struct {
			Issue  backendstore.Issue `json:"issue"`
			Noop   bool               `json:"noop"`
			Review struct {
				ID       string `json:"id"`
				Decision string `json:"decision"`
			} `json:"review"`
		}
		var observed backendstore.Issue
		identity := struct {
			Command any
			Index   int
		}{backendInvocation(cmd, args, action), index}
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
			if action == "approve" && recordOnly {
				detailResult, e := c.Detail(cmd.Context(), id)
				if e != nil {
					return backendstore.MutationRequest{}, e
				}
				detail := detailResult.Issue
				observed = detail
				var reviews []struct {
					Decision        string  `json:"decision"`
					Summary         string  `json:"summary"`
					ReviewerSession string  `json:"reviewer_session"`
					ReviewedBy      string  `json:"reviewed_by"`
					SelfReview      bool    `json:"self_review"`
					SupersededAt    *string `json:"superseded_at"`
				}
				if e := json.Unmarshal(detail.Reviews, &reviews); e != nil {
					return backendstore.MutationRequest{}, fmt.Errorf("decode backend reviews: %w", e)
				}
				for _, review := range reviews {
					if detail.Status == models.StatusInReview && review.SupersededAt == nil && review.Decision == decision && review.Summary == strings.TrimSpace(reason) && review.ReviewerSession == c.SessionID && review.ReviewedBy == strings.TrimSpace(reviewedBy) && review.SelfReview == selfReview {
						return backendstore.MutationRequest{}, errBackendTransitionUnchanged
					}
				}
			}
			if action == "start" && observed.Status == target && observed.ImplementerSession == c.SessionID {
				return backendstore.MutationRequest{}, errBackendTransitionUnchanged
			}
			if action == "unstart" && observed.Status == target && observed.ImplementerSession == "" {
				return backendstore.MutationRequest{}, errBackendTransitionUnchanged
			}
			if (action == "block" || action == "unblock" || action == "reopen" || action == "review" || action == "close" || action == "reject" || (action == "approve" && !recordOnly)) && observed.Status == target {
				return backendstore.MutationRequest{}, errBackendTransitionUnchanged
			}
			body := map[string]any{}
			if reason != "" {
				body["reason"] = reason
			}
			if force {
				body["force"] = true
			}
			resource := "/issues/" + id + "/" + action
			if action == "approve" {
				if recordOnly {
					resource = "/issues/" + id + "/reviews"
					body = map[string]any{"decision": decision, "summary": reason}
				}
				if selfReview {
					body["self_review"] = true
				}
				if reviewedBy != "" {
					body["reviewed_by"] = reviewedBy
				}
			}
			if action == "close" {
				if admin, _ := cmd.Flags().GetString("admin"); admin != "" {
					body["admin_reason"] = admin
				}
				if exception, _ := cmd.Flags().GetString("self-close-exception"); exception != "" {
					body["self_close_exception"] = exception
				}
			}
			raw, e := json.Marshal(body)
			if e != nil {
				return backendstore.MutationRequest{}, e
			}
			return backendstore.MutationRequest{Method: "POST", Resource: resource, Revision: observed.Revision, Body: raw}, nil
		}, &reply, func() error {
			if e := backendstore.ValidateIssue(reply.Issue); e != nil {
				return e
			}
			requested := strings.TrimSpace(rawID)
			if !strings.HasPrefix(requested, "td-") {
				requested = "td-" + requested
			}
			if !strings.HasPrefix(reply.Issue.ID, requested) || reply.Issue.Status != target {
				return fmt.Errorf("backend workflow response differs from request")
			}
			if action == "approve" && recordOnly && (reply.Review.ID == "" || reply.Review.Decision != decision) {
				return fmt.Errorf("backend review response differs from request")
			}
			if action == "start" && reply.Issue.ImplementerSession != c.SessionID {
				return fmt.Errorf("backend started Issue under a different Session")
			}
			if action == "unstart" && reply.Issue.ImplementerSession != "" {
				return fmt.Errorf("backend retained implementer after unstart")
			}
			return nil
		})
		noop := errors.Is(e, errBackendTransitionUnchanged)
		if noop {
			e = nil
			reply.Issue = observed
		}
		if e != nil {
			if len(completed) > 0 {
				return fmt.Errorf("%s completed for %s; stopped at %s (earlier changes remain; retry the same command): %w", action, strings.Join(completed, ", "), rawID, e)
			}
			return e
		}
		completed = append(completed, reply.Issue.ID)
		if jsonMode(cmd) {
			if e := json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"id": reply.Issue.ID, "status": reply.Issue.Status, "action": action, "issue": reply.Issue.Issue, "noop": noop || reply.Noop}); e != nil {
				return e
			}
		} else {
			if _, e := fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", label, reply.Issue.ID); e != nil {
				return e
			}
		}
	}
	return nil
}

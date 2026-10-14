package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/models"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

type backendWSHandoffEntry struct {
	IssueID   string   `json:"issue_id"`
	Done      []string `json:"done"`
	Remaining []string `json:"remaining"`
	Decisions []string `json:"decisions"`
	Uncertain []string `json:"uncertain"`
}

type backendWSHandoffPlan struct {
	WorkSessionID string                  `json:"work_session_id"`
	IssueIDs      []string                `json:"issue_ids"`
	Entries       []backendWSHandoffEntry `json:"entries"`
	CommitSHA     string                  `json:"commit_sha"`
	Branch        string                  `json:"branch"`
	DirtyFiles    int                     `json:"dirty_files"`
	Continue      bool                    `json:"continue"`
	Review        bool                    `json:"review"`
}

var errBackendWSReviewUnchanged = errors.New("issue is not in progress")

func init() {
	local := wsHandoffCmd.RunE
	wsHandoffCmd.RunE = func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(getBaseDir())
		if err != nil {
			return err
		}
		kind, err := config.Store(cfg)
		if err != nil {
			return err
		}
		if kind == config.StoreBackend {
			return runBackendWSHandoff(cmd, args)
		}
		return local(cmd, args)
	}
}

func runBackendWSHandoff(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	if len(args) != 0 {
		return fmt.Errorf("ws handoff takes no arguments")
	}
	allowed := " json work-dir help refresh done remaining decision uncertain continue review "
	var flagErr error
	cmd.Flags().Visit(func(flag *pflag.Flag) {
		if !strings.Contains(allowed, " "+flag.Name+" ") {
			flagErr = fmt.Errorf("td-backend does not support --%s for ws handoff", flag.Name)
		}
	})
	if flagErr != nil {
		return flagErr
	}
	c, err := openBackendCommand(cmd)
	if err != nil {
		return err
	}
	var completed, reviewed []string
	var plan backendWSHandoffPlan
	err = c.RunGroupIntent(cmd.Context(), backendInvocation(cmd, args, "ws.handoff"), func() (json.RawMessage, error) {
		var err error
		plan, err = prepareBackendWSHandoff(cmd, c)
		if err != nil {
			return nil, err
		}
		return json.Marshal(plan)
	}, func(groupID string, stored json.RawMessage) error {
		if err := json.Unmarshal(stored, &plan); err != nil {
			return fmt.Errorf("invalid stored work-session handoff plan: %w", err)
		}
		if err := validateBackendWSHandoffPlan(plan); err != nil {
			return err
		}
		completed, reviewed = []string{}, []string{}
		return applyBackendWSHandoff(cmd, c, groupID, plan, &completed, &reviewed)
	})
	if err != nil {
		return err
	}
	ended := !plan.Continue
	if jsonMode(cmd) {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"action": "handed_off_work_session", "work_session": plan.WorkSessionID, "issues": completed, "ended": ended, "review_requested": plan.Review, "review_requests": reviewed})
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "HANDOFF RECORDED %s → %s\n", plan.WorkSessionID, strings.Join(completed, ", "))
	if err == nil && ended {
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "WORK SESSION ENDED %s\n", plan.WorkSessionID)
	}
	return err
}

func prepareBackendWSHandoff(cmd *cobra.Command, c *backendstore.Connection) (backendWSHandoffPlan, error) {
	var plan backendWSHandoffPlan
	id, err := c.ActiveWorkSessionID(cmd.Context())
	if err != nil {
		return plan, err
	}
	if id == "" {
		return plan, fmt.Errorf("no active work session")
	}
	detail, err := c.WorkSession(cmd.Context(), id)
	if err != nil {
		return plan, err
	}
	if detail.WorkSession.SessionID != c.SessionID || detail.WorkSession.EndedAt != nil {
		return plan, fmt.Errorf("active work session changed")
	}
	plan.WorkSessionID, plan.IssueIDs = id, slices.Clone(detail.IssueIDs)
	plan.Continue, _ = cmd.Flags().GetBool("continue")
	plan.Review, _ = cmd.Flags().GetBool("review")
	if plan.Review && len(plan.IssueIDs) == 0 {
		return plan, fmt.Errorf("ws handoff --review requires at least one tagged issue")
	}
	input := models.Activity{Kind: "handoff"}
	if err := readGitHubHandoff(cmd, &input); err != nil {
		return plan, err
	}
	explicit := len(input.Done)+len(input.Remaining)+len(input.Decisions)+len(input.Uncertain) > 0
	if len(plan.IssueIDs) == 0 && (explicit || plan.Continue) {
		return plan, fmt.Errorf("ws handoff needs tagged issues to record content or continue")
	}
	logs := []models.Log{}
	activities := []models.Activity{}
	if !explicit && len(plan.IssueIDs) > 0 {
		logs, err = c.WorkSessionLogs(cmd.Context(), id)
		if err != nil {
			return plan, err
		}
		activities, err = c.WorkSessionActivity(cmd.Context(), id)
		if err != nil {
			return plan, err
		}
	}
	for _, issueID := range plan.IssueIDs {
		issue, err := c.Client.GetIssue(cmd.Context(), c.ProjectID, c.SessionID, issueID)
		if err != nil {
			return plan, err
		}
		if issue.Status == models.StatusClosed {
			return plan, fmt.Errorf("cannot hand off closed issue %s", issueID)
		}
		entry := backendWSHandoffEntry{IssueID: issueID, Done: slices.Clone(input.Done), Remaining: filterGitHubWSRemaining(input.Remaining, issueID), Decisions: slices.Clone(input.Decisions), Uncertain: slices.Clone(input.Uncertain)}
		if !explicit {
			for _, log := range logs {
				addBackendWSHandoffLog(&entry, log.Type, log.Message)
			}
			for _, activity := range activities {
				if activity.Kind == "log" && activity.IssueID == issueID {
					addBackendWSHandoffLog(&entry, activity.LogType, activity.Message)
				}
			}
		}
		for _, items := range []*[]string{&entry.Done, &entry.Remaining, &entry.Decisions, &entry.Uncertain} {
			if *items == nil {
				*items = []string{}
			}
		}
		for _, items := range [][]string{entry.Done, entry.Remaining, entry.Decisions, entry.Uncertain} {
			for _, item := range items {
				if strings.TrimSpace(item) == "" {
					return plan, fmt.Errorf("handoff items must not be empty")
				}
			}
		}
		if len(entry.Done)+len(entry.Remaining)+len(entry.Decisions)+len(entry.Uncertain) == 0 {
			return plan, fmt.Errorf("handoff has no content for %s; provide flags or record work-session logs first", issueID)
		}
		plan.Entries = append(plan.Entries, entry)
	}
	snapshot, err := gitHubSnapshot(cmd.Context(), c.Worktree)
	if err != nil {
		return plan, err
	}
	plan.CommitSHA, plan.Branch, plan.DirtyFiles = snapshot.CommitSHA, snapshot.Branch, snapshot.DirtyFiles
	return plan, nil
}

func addBackendWSHandoffLog(entry *backendWSHandoffEntry, kind models.LogType, message string) {
	switch kind {
	case models.LogTypeProgress, models.LogTypeResult:
		entry.Done = append(entry.Done, message)
	case models.LogTypeDecision:
		entry.Decisions = append(entry.Decisions, message)
	case models.LogTypeBlocker:
		entry.Uncertain = append(entry.Uncertain, message)
	}
}

func validateBackendWSHandoffPlan(plan backendWSHandoffPlan) error {
	if plan.WorkSessionID == "" || len(plan.IssueIDs) != len(plan.Entries) || plan.Review && len(plan.IssueIDs) == 0 {
		return fmt.Errorf("invalid stored work-session handoff plan")
	}
	for index, entry := range plan.Entries {
		if !backendstore.ValidIssueID(entry.IssueID) || plan.IssueIDs[index] != entry.IssueID || len(entry.Done)+len(entry.Remaining)+len(entry.Decisions)+len(entry.Uncertain) == 0 {
			return fmt.Errorf("invalid stored work-session handoff entry")
		}
	}
	return nil
}

func applyBackendWSHandoff(cmd *cobra.Command, c *backendstore.Connection, groupID string, plan backendWSHandoffPlan, completed, reviewed *[]string) error {
	checkActive := func() error {
		detail, err := c.WorkSession(cmd.Context(), plan.WorkSessionID)
		if err != nil {
			return err
		}
		if detail.WorkSession.SessionID != c.SessionID || detail.WorkSession.EndedAt != nil || !slices.Equal(detail.IssueIDs, plan.IssueIDs) {
			return fmt.Errorf("active work session or tags changed; reread before retrying")
		}
		return nil
	}
	snapshot := map[string]any{"commit_sha": plan.CommitSHA, "branch": plan.Branch, "dirty_files": plan.DirtyFiles}
	for _, entry := range plan.Entries {
		var reply struct {
			Activity models.Activity `json:"activity"`
		}
		identity := struct{ GroupID, Phase, IssueID string }{groupID, "handoff", entry.IssueID}
		err := c.RunGroupStepIntent(cmd.Context(), identity, func() (backendstore.MutationRequest, error) {
			if err := checkActive(); err != nil {
				return backendstore.MutationRequest{}, err
			}
			issue, err := c.Client.GetIssue(cmd.Context(), c.ProjectID, c.SessionID, entry.IssueID)
			if err != nil {
				return backendstore.MutationRequest{}, err
			}
			body, err := json.Marshal(map[string]any{"kind": "handoff", "done": entry.Done, "remaining": entry.Remaining, "decisions": entry.Decisions, "uncertain": entry.Uncertain, "work_session_id": plan.WorkSessionID, "snapshot": snapshot})
			if err != nil {
				return backendstore.MutationRequest{}, err
			}
			return backendstore.MutationRequest{Method: "POST", Resource: "/issues/" + entry.IssueID + "/activity", Revision: issue.Revision, Body: body}, nil
		}, &reply, func() error {
			a := reply.Activity
			if a.ID == "" || a.IssueID != entry.IssueID || a.SessionID != c.SessionID || a.WorkSessionID != plan.WorkSessionID || a.Kind != "handoff" || a.CreatedAt.IsZero() || !slices.Equal(a.Done, entry.Done) || !slices.Equal(a.Remaining, entry.Remaining) || !slices.Equal(a.Decisions, entry.Decisions) || !slices.Equal(a.Uncertain, entry.Uncertain) {
				return fmt.Errorf("invalid backend work-session handoff response for %s", entry.IssueID)
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("ws handoff recorded on %s; stopped at %s (retry the same command): %w", strings.Join(*completed, ", "), entry.IssueID, err)
		}
		*completed = append(*completed, entry.IssueID)
	}
	if plan.Review {
		for _, entry := range plan.Entries {
			var reply struct {
				Issue backendstore.Issue `json:"issue"`
			}
			identity := struct{ GroupID, Phase, IssueID string }{groupID, "review", entry.IssueID}
			err := c.RunGroupStepIntent(cmd.Context(), identity, func() (backendstore.MutationRequest, error) {
				if err := checkActive(); err != nil {
					return backendstore.MutationRequest{}, err
				}
				issue, err := c.Client.GetIssue(cmd.Context(), c.ProjectID, c.SessionID, entry.IssueID)
				if err != nil {
					return backendstore.MutationRequest{}, err
				}
				if issue.Status != models.StatusInProgress {
					return backendstore.MutationRequest{}, errBackendWSReviewUnchanged
				}
				body, err := json.Marshal(map[string]string{"reason": "Submitted for review via ws handoff --review"})
				if err != nil {
					return backendstore.MutationRequest{}, err
				}
				return backendstore.MutationRequest{Method: "POST", Resource: "/issues/" + entry.IssueID + "/review", Revision: issue.Revision, Body: body}, nil
			}, &reply, func() error {
				if err := backendstore.ValidateIssue(reply.Issue); err != nil {
					return err
				}
				if reply.Issue.ID != entry.IssueID || reply.Issue.Status != models.StatusInReview {
					return fmt.Errorf("backend review response differs for %s", entry.IssueID)
				}
				return nil
			})
			if errors.Is(err, errBackendWSReviewUnchanged) {
				continue
			}
			if err != nil {
				return fmt.Errorf("ws handoff recorded on %s; reviews requested for %s; review stopped at %s (retry the same command): %w", strings.Join(*completed, ", "), strings.Join(*reviewed, ", "), entry.IssueID, err)
			}
			*reviewed = append(*reviewed, entry.IssueID)
		}
	}
	if plan.Continue {
		return nil
	}
	var reply struct {
		WorkSession backendstore.WorkSession `json:"work_session"`
	}
	identity := struct{ GroupID, Phase, WorkSessionID string }{groupID, "end", plan.WorkSessionID}
	err := c.RunGroupStepIntent(cmd.Context(), identity, func() (backendstore.MutationRequest, error) {
		if err := checkActive(); err != nil {
			return backendstore.MutationRequest{}, err
		}
		body, err := json.Marshal(map[string]string{"end_sha": plan.CommitSHA})
		if err != nil {
			return backendstore.MutationRequest{}, err
		}
		return backendstore.MutationRequest{Method: "POST", Resource: "/work-sessions/" + plan.WorkSessionID + "/end", Body: body}, nil
	}, &reply, func() error {
		if err := backendstore.ValidateWorkSession(reply.WorkSession); err != nil {
			return err
		}
		if reply.WorkSession.ID != plan.WorkSessionID || reply.WorkSession.SessionID != c.SessionID || reply.WorkSession.EndedAt == nil {
			return fmt.Errorf("backend did not end the work session")
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("ws handoff recorded on %s; reviews requested for %s; work-session end failed (retry the same command): %w", strings.Join(*completed, ", "), strings.Join(*reviewed, ", "), err)
	}
	return nil
}

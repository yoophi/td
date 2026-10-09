package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/issuestore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/query"
	"github.com/marcus/td/internal/reviewpolicy"
	"slices"
	"strings"
	"time"
)

// GitHubMonitorReader provides the shared read observations for web and TUI.
// Reads include deleted tasks so their activity remains visible.
type GitHubMonitorReader interface {
	issuestore.ActivityStore
	ListIncludingDeleted(context.Context, bool) ([]ghstore.Record, error)
	ListActivityIncludingDeleted(context.Context, string) ([]models.Activity, error)
	ObserveMonitorReview(context.Context, *ghstore.Record, string) (*ghstore.MonitorReviewFacts, error)
}

type monitorActivityCache struct {
	GitHubMonitorReader
	activities map[string][]models.Activity
}

func (c *monitorActivityCache) ListActivity(_ context.Context, id string) ([]models.Activity, error) {
	n, err := ghstore.Number(id)
	if err != nil {
		return nil, err
	}
	activities, ok := c.activities[fmt.Sprintf("gh-%d", n)]
	if !ok {
		return nil, fmt.Errorf("monitor activity observation missing for %s", id)
	}
	return activities, nil
}
func FetchGitHubData(ctx context.Context, c GitHubMonitorReader, actor string, mode reviewpolicy.Mode, focus *string, search, searchMode string, includeClosed bool, sortMode SortMode, now time.Time) (*RefreshDataMsg, error) {
	var observed *ghstore.Snapshot
	var records []ghstore.Record
	var err error
	if bulk, ok := c.(ghstore.SnapshotReader); ok {
		observed, err = bulk.ReadSnapshot(ctx, true)
		if err == nil && observed == nil {
			err = fmt.Errorf("GitHub snapshot observation missing")
		}
		if err == nil {
			records = observed.Issues(true)
		}
	} else {
		records, err = c.ListIncludingDeleted(ctx, true)
	}
	if err != nil {
		return nil, err
	}
	msg := &RefreshDataMsg{Timestamp: now, observedIssues: map[string]ghstore.Record{}}
	cache := &monitorActivityCache{GitHubMonitorReader: c, activities: map[string][]models.Activity{}}
	byID := map[string]ghstore.Record{}
	seenActivity := map[string]bool{}
	active := map[string]time.Time{}
	visible := []models.Issue{}
	for _, record := range records {
		if _, ok := byID[record.ID]; ok {
			return nil, fmt.Errorf("monitor listing repeated %s", record.ID)
		}
		byID[record.ID] = record
		if record.DeletedAt == nil {
			msg.observedIssues[record.ID] = record
			visible = append(visible, record.Issue)
			msg.HasIssues = true
			if focus != nil && record.ID == *focus {
				copy := record.Issue
				msg.FocusedIssue = &copy
			}
			if record.Status == models.StatusInProgress {
				msg.InProgress = append(msg.InProgress, record.Issue)
			}
		}
		var activities []models.Activity
		if observed != nil {
			activities, err = observed.Activity(record.ID)
		} else {
			activities, err = c.ListActivityIncludingDeleted(ctx, record.ID)
		}
		if err != nil {
			return nil, fmt.Errorf("monitor activities on %s: %w", record.ID, err)
		}
		cache.activities[record.ID] = activities
		for _, a := range activities {
			if seenActivity[a.ID] {
				return nil, fmt.Errorf("monitor activity repeated %s", a.ID)
			}
			seenActivity[a.ID] = true
			switch a.Kind {
			case "log", "comment":
				msg.Activity = append(msg.Activity, ActivityItem{Timestamp: a.CreatedAt, SessionID: a.SessionID, Type: a.Kind, IssueID: record.ID, IssueTitle: record.Title, Message: a.Message, LogType: a.LogType, EntityID: a.ID})
				if a.Kind == "log" && a.SessionID != "" && a.CreatedAt.After(now.Add(-5*time.Minute)) && a.CreatedAt.After(active[a.SessionID]) {
					active[a.SessionID] = a.CreatedAt
				}
			case "handoff":
				if a.CreatedAt.After(now.Add(-24 * time.Hour)) {
					msg.RecentHandoffs = append(msg.RecentHandoffs, RecentHandoff{IssueID: record.ID, SessionID: a.SessionID, Timestamp: a.CreatedAt})
				}
				msg.Activity = append(msg.Activity, ActivityItem{Timestamp: a.CreatedAt, SessionID: a.SessionID, Type: "action", IssueID: record.ID, IssueTitle: record.Title, Action: models.ActionHandoff, EntityID: a.ID, EntityType: "handoff", Message: "Handoff"})
			default:
				return nil, fmt.Errorf("unsupported monitor activity kind %q", a.Kind)
			}
		}
		if record.Details != nil {
			for _, event := range record.Details.Transitions {
				previous, _ := json.Marshal(map[string]any{"status": event.From})
				next, _ := json.Marshal(map[string]any{"status": event.To})
				msg.Activity = append(msg.Activity, ActivityItem{Timestamp: event.At, SessionID: event.SessionID, Type: "action", IssueID: record.ID, IssueTitle: record.Title, Action: models.ActionType(event.Action), EntityID: event.OperationID, EntityType: "issue", Message: event.Action + " " + record.ID, PreviousData: string(previous), NewData: string(next)})
			}
		}
	}
	snapshot, err := issuestore.NewGitHubQuerySnapshot(ctx, records, cache)
	if err != nil {
		return nil, err
	}
	sortBy, sortDesc := sortMode.ToDBOptions()
	usedTDQ := false
	if search != "" && searchMode != "text" {
		parsed, parseErr := query.Parse(search)
		if parseErr == nil && len(parsed.Validate()) == 0 {
			result, err := query.ExecuteDetailed(snapshot, search, actor, query.ExecuteOptions{MaxResults: len(records) + 1, SortBy: sortBy, SortDesc: sortDesc})
			if err != nil {
				return nil, err
			}
			visible = result.Issues
			usedTDQ = true
		} else if searchMode == "tdq" {
			return nil, fmt.Errorf("invalid monitor TDQ query")
		}
	}
	if search != "" && !usedTDQ {
		filtered := []models.Issue{}
		for _, issue := range visible {
			if strings.Contains(strings.ToLower(issue.Title+"\n"+issue.Description), strings.ToLower(search)) {
				filtered = append(filtered, issue)
			}
		}
		visible = filtered
	}
	if !usedTDQ {
		if err := issuestore.SortGitHubIssues(visible, sortBy, sortDesc); err != nil {
			return nil, err
		}
	}
	if err := issuestore.SortGitHubIssues(msg.InProgress, "priority", false); err != nil {
		return nil, err
	}
	for _, issue := range visible {
		record := byID[issue.ID]
		switch issue.Status {
		case models.StatusOpen:
			blocked := false
			if record.Details != nil {
				for _, id := range record.Details.Dependencies {
					target, ok := byID[id]
					if !ok || target.DeletedAt != nil {
						return nil, fmt.Errorf("monitor dependency %s of %s is missing or deleted", id, issue.ID)
					}
					if target.Status != models.StatusClosed {
						blocked = true
					}
				}
			}
			if blocked {
				msg.TaskList.Blocked = append(msg.TaskList.Blocked, issue)
			} else {
				msg.TaskList.Ready = append(msg.TaskList.Ready, issue)
			}
		case models.StatusInProgress:
			rejected := false
			var lastReject, lastReview time.Time
			if record.Details != nil {
				for _, event := range record.Details.Transitions {
					if event.Action == "reject" {
						rejected = true
						if event.At.After(lastReject) {
							lastReject = event.At
						}
					}
					if event.Action == "review" && event.At.After(lastReview) {
						lastReview = event.At
					}
				}
			}
			rejected = rejected && !lastReview.After(lastReject)
			if rejected {
				msg.TaskList.NeedsRework = append(msg.TaskList.NeedsRework, issue)
			} else {
				msg.TaskList.InProgress = append(msg.TaskList.InProgress, issue)
			}
		case models.StatusBlocked:
			msg.TaskList.Blocked = append(msg.TaskList.Blocked, issue)
		case models.StatusInReview:
			var facts *ghstore.MonitorReviewFacts
			var err error
			if observed != nil {
				facts, err = ghstore.UnverifiedMonitorReview(&record, actor)
				msg.TaskList.ReviewVerificationRequired = true
			} else {
				facts, err = c.ObserveMonitorReview(ctx, &record, actor)
			}
			if err != nil {
				return nil, err
			}
			if facts == nil {
				return nil, fmt.Errorf("missing review observation for %s", issue.ID)
			}
			category := CategoryPendingOther
			if facts.Fresh {
				category = CategorizeInReview(&issue, actor, mode, facts.ImplementationInvolved, facts.AnyInvolved, facts.ActiveApproval)
			}
			switch category {
			case CategoryReviewable:
				msg.TaskList.Reviewable = append(msg.TaskList.Reviewable, issue)
			case CategoryReadyToClose:
				msg.TaskList.ReadyToClose = append(msg.TaskList.ReadyToClose, issue)
			case CategoryPendingReview:
				msg.TaskList.PendingReview = append(msg.TaskList.PendingReview, issue)
			default:
				msg.TaskList.PendingOther = append(msg.TaskList.PendingOther, issue)
			}
		case models.StatusClosed:
			if includeClosed {
				msg.TaskList.Closed = append(msg.TaskList.Closed, issue)
			}
		default:
			return nil, fmt.Errorf("invalid monitor issue status %q", issue.Status)
		}
	}
	slices.SortFunc(msg.Activity, func(a, b ActivityItem) int {
		if cmp := b.Timestamp.Compare(a.Timestamp); cmp != 0 {
			return cmp
		}
		return strings.Compare(a.EntityID, b.EntityID)
	})
	if len(msg.Activity) > 50 {
		msg.Activity = msg.Activity[:50]
	}
	slices.SortFunc(msg.RecentHandoffs, func(a, b RecentHandoff) int {
		if cmp := b.Timestamp.Compare(a.Timestamp); cmp != 0 {
			return cmp
		}
		return strings.Compare(a.IssueID, b.IssueID)
	})
	if len(msg.RecentHandoffs) > 10 {
		msg.RecentHandoffs = msg.RecentHandoffs[:10]
	}
	for session := range active {
		msg.ActiveSessions = append(msg.ActiveSessions, session)
	}
	slices.SortFunc(msg.ActiveSessions, func(a, b string) int {
		if cmp := active[b].Compare(active[a]); cmp != 0 {
			return cmp
		}
		return strings.Compare(a, b)
	})
	return msg, nil
}

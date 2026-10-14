package monitor

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/internal/models"
)

type backendMonitorReader interface {
	Monitor(context.Context, string, bool, string) (backendstore.Monitor, error)
}

// BackendDataSource owns a single in-flight dashboard request. The guard
// checks the active project/worktree/session both before and after I/O so a
// result from an obsolete context never replaces the visible dashboard.
type BackendDataSource struct {
	ctx     context.Context
	reader  backendMonitorReader
	guard   func(context.Context) error
	refresh sync.Mutex
	running atomic.Bool
}

func NewBackendDataSource(ctx context.Context, reader backendMonitorReader, guard func(context.Context) error) *BackendDataSource {
	return &BackendDataSource{ctx: ctx, reader: reader, guard: guard}
}

func (s *BackendDataSource) Fetch(search string, includeClosed bool, sortMode SortMode) (result RefreshDataMsg) {
	if !s.refresh.TryLock() {
		return RefreshDataMsg{Skipped: true}
	}
	defer s.refresh.Unlock()
	s.running.Store(true)
	defer s.running.Store(false)
	defer func() { result.remoteFilter = &monitorRefreshFilter{search, includeClosed, sortMode} }()
	if err := s.ctx.Err(); err != nil {
		return RefreshDataMsg{Error: err}
	}
	if s.reader == nil || s.guard == nil {
		return RefreshDataMsg{Error: fmt.Errorf("backend monitor is not configured")}
	}
	if err := s.guard(s.ctx); err != nil {
		return RefreshDataMsg{Error: err}
	}
	sortField := "priority"
	switch sortMode {
	case SortByCreatedDesc:
		sortField = "-created"
	case SortByUpdatedDesc:
		sortField = "-updated"
	}
	snapshot, err := s.reader.Monitor(s.ctx, search, includeClosed, sortField)
	if err != nil {
		return RefreshDataMsg{Error: err}
	}
	if err := s.guard(s.ctx); err != nil {
		return RefreshDataMsg{Error: err}
	}
	result = backendRefreshData(snapshot)
	result.Transitions = s.backendTransitionHandles(snapshot)
	return result
}

func (s *BackendDataSource) IsRefreshing() bool { return s.running.Load() }

func backendRefreshData(snapshot backendstore.Monitor) RefreshDataMsg {
	m := snapshot.Dashboard
	msg := RefreshDataMsg{Timestamp: m.Timestamp, ObservationMode: m.ObservationMode, Activity: make([]ActivityItem, 0, len(m.Activity)), RecentHandoffs: make([]RecentHandoff, 0, len(m.RecentHandoffs)), ActiveSessions: m.ActiveSessions, InProgress: backendIssues(m.InProgress), Transitions: map[string]MonitorTransitionStore{}}
	if parsed, err := time.Parse(time.RFC3339Nano, m.FullReconciledAt); err == nil {
		msg.FullReconciledAt = parsed
	}
	if m.FocusedIssue != nil {
		focused := m.FocusedIssue.Issue
		msg.FocusedIssue = &focused
		msg.HasIssues = true
	}
	msg.TaskList = TaskListData{ReviewVerificationRequired: m.TaskList.ReviewVerificationRequired, Reviewable: backendIssues(m.TaskList.Reviewable), NeedsRework: backendIssues(m.TaskList.NeedsRework), InProgress: backendIssues(m.TaskList.InProgress), Ready: backendIssues(m.TaskList.Ready), PendingReview: backendIssues(m.TaskList.PendingReview), ReadyToClose: backendIssues(m.TaskList.ReadyToClose), PendingOther: backendIssues(m.TaskList.PendingOther), Blocked: backendIssues(m.TaskList.Blocked), Closed: backendIssues(m.TaskList.Closed)}
	for _, row := range m.Activity {
		msg.Activity = append(msg.Activity, ActivityItem{Timestamp: row.Timestamp.Local(), SessionID: row.SessionID, Type: row.Type, IssueID: row.IssueID, IssueTitle: row.IssueTitle, Message: row.Message, LogType: models.LogType(row.LogType), Action: models.ActionType(row.Action), EntityID: row.EntityID, EntityType: row.EntityType, PreviousData: row.PreviousData, NewData: row.NewData})
	}
	for _, row := range m.RecentHandoffs {
		msg.RecentHandoffs = append(msg.RecentHandoffs, RecentHandoff{IssueID: row.IssueID, SessionID: row.SessionID, Timestamp: row.Timestamp.Local()})
	}
	for _, rows := range [][]models.Issue{msg.InProgress, msg.TaskList.Reviewable, msg.TaskList.NeedsRework, msg.TaskList.InProgress, msg.TaskList.Ready, msg.TaskList.PendingReview, msg.TaskList.ReadyToClose, msg.TaskList.PendingOther, msg.TaskList.Blocked, msg.TaskList.Closed} {
		if len(rows) > 0 {
			msg.HasIssues = true
			break
		}
	}
	return msg
}

func backendIssues(rows []backendstore.Issue) []models.Issue {
	out := make([]models.Issue, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Issue)
	}
	return out
}

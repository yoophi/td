package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/internal/models"
)

type backendDetailReader interface {
	Detail(context.Context, string) (backendstore.Detail, error)
	List(context.Context, bool) ([]backendstore.Issue, error)
}

// Details builds the monitor modal from the server's detail response. A
// complete list resolves the linked issues; any missing reference aborts the
// modal update instead of displaying an inconsistent partial relationship.
func (s *BackendDataSource) Details(id string) IssueDetailsMsg {
	fail := func(err error) IssueDetailsMsg { return IssueDetailsMsg{IssueID: id, Error: err} }
	if !backendstore.ValidIssueID(id) {
		return fail(fmt.Errorf("monitor details require a canonical backend issue ID"))
	}
	if err := s.ctx.Err(); err != nil {
		return fail(err)
	}
	if s.guard == nil {
		return fail(fmt.Errorf("backend monitor context unavailable"))
	}
	if err := s.guard(s.ctx); err != nil {
		return fail(err)
	}
	reader, ok := s.reader.(backendDetailReader)
	if !ok {
		return fail(fmt.Errorf("backend detail reader unavailable"))
	}
	detail, err := reader.Detail(s.ctx, id)
	if err != nil {
		return fail(err)
	}
	rows, err := reader.List(s.ctx, true)
	if err != nil {
		return fail(err)
	}
	if err := s.guard(s.ctx); err != nil {
		return fail(err)
	}
	byID := make(map[string]backendstore.Issue, len(rows))
	for _, row := range rows {
		byID[row.ID] = row
	}
	if observed, ok := byID[id]; !ok || observed.Revision != detail.Issue.Revision {
		return fail(fmt.Errorf("backend issue changed during detail refresh; retry"))
	}
	issue := detail.Issue.Issue
	msg := IssueDetailsMsg{IssueID: id, Issue: &issue, Handoff: detail.LatestHandoff, Logs: detail.Logs, Comments: make([]models.Comment, 0, len(detail.Comments)), EpicTasks: backendIssues(detail.Children)}
	for _, comment := range detail.Comments {
		msg.Comments = append(msg.Comments, models.Comment{ID: comment.ID, IssueID: comment.IssueID, SessionID: comment.SessionID, Text: comment.Text, CreatedAt: comment.CreatedAt})
	}
	for _, relation := range detail.Dependencies {
		if relation.IssueID != id {
			return fail(fmt.Errorf("invalid backend dependency direction"))
		}
		row, ok := byID[relation.DependsOnID]
		if !ok {
			return fail(fmt.Errorf("detail dependency %s unavailable", relation.DependsOnID))
		}
		msg.BlockedBy = append(msg.BlockedBy, row.Issue)
	}
	for _, relation := range detail.BlockedBy {
		if relation.DependsOnID != id {
			return fail(fmt.Errorf("invalid backend dependent direction"))
		}
		row, ok := byID[relation.IssueID]
		if !ok {
			return fail(fmt.Errorf("detail dependent %s unavailable", relation.IssueID))
		}
		msg.Blocks = append(msg.Blocks, row.Issue)
	}
	if issue.ParentID != "" {
		parent, ok := byID[issue.ParentID]
		if !ok || parent.Type != models.TypeEpic {
			return fail(fmt.Errorf("detail parent %s unavailable", issue.ParentID))
		}
		msg.ParentEpic = &parent.Issue
	}
	if len(detail.Issue.Reviews) > 0 && string(detail.Issue.Reviews) != "null" {
		var reviews []models.IssueReview
		if err := json.Unmarshal(detail.Issue.Reviews, &reviews); err != nil {
			return fail(fmt.Errorf("invalid backend review history: %w", err))
		}
		for i := range reviews {
			msg.Reviews = append(msg.Reviews, &reviews[i])
		}
	}
	msg.HasActiveApproval = len(detail.Issue.ActiveReview) > 0 && string(detail.Issue.ActiveReview) != "null"
	for _, issues := range [][]models.Issue{msg.EpicTasks, msg.BlockedBy, msg.Blocks} {
		sort.Slice(issues, func(i, j int) bool { return issues[i].ID < issues[j].ID })
	}
	return msg
}

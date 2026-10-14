package issuestore

import (
	"context"
	"fmt"
	"reflect"
	"slices"

	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/internal/db"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/query"
)

// BackendQuerySnapshot is request-local. Use it inside Connection.ReadSnapshot
// so lazy detail and policy reads share the observed project change token.
type BackendQuerySnapshot struct {
	ctx        context.Context
	connection *backendstore.Connection
	rows       map[string]backendstore.Issue
	details    map[string]backendstore.Detail
}

var _ query.QuerySource = (*BackendQuerySnapshot)(nil)

func NewBackendQuerySnapshot(ctx context.Context, c *backendstore.Connection, rows []backendstore.Issue) (*BackendQuerySnapshot, error) {
	out := &BackendQuerySnapshot{ctx: ctx, connection: c, rows: map[string]backendstore.Issue{}, details: map[string]backendstore.Detail{}}
	for _, row := range rows {
		if _, ok := out.rows[row.ID]; ok {
			return nil, fmt.Errorf("duplicate backend snapshot issue")
		}
		out.rows[row.ID] = row
	}
	return out, nil
}
func (s *BackendQuerySnapshot) GetIssue(id string) (*models.Issue, error) {
	if e := s.ctx.Err(); e != nil {
		return nil, e
	}
	row, ok := s.rows[id]
	if !ok {
		return nil, fmt.Errorf("backend issue %s not found in snapshot", id)
	}
	copy := row.Issue
	return &copy, nil
}
func (s *BackendQuerySnapshot) ListIssues(o db.ListIssuesOptions) ([]models.Issue, error) {
	rest := o
	rest.SortBy = ""
	rest.SortDesc = false
	rest.Limit = 0
	if !reflect.DeepEqual(rest, db.ListIssuesOptions{}) {
		return nil, fmt.Errorf("backend TDQ snapshot accepts sorting and limit only")
	}
	if e := s.ctx.Err(); e != nil {
		return nil, e
	}
	out := make([]models.Issue, 0, len(s.rows))
	for _, row := range s.rows {
		out = append(out, row.Issue)
	}
	if e := SortGitHubIssues(out, o.SortBy, o.SortDesc); e != nil {
		return nil, e
	}
	if o.Limit > 0 && len(out) > o.Limit {
		out = out[:o.Limit]
	}
	return out, nil
}
func (s *BackendQuerySnapshot) detail(id string) (backendstore.Detail, error) {
	if _, e := s.GetIssue(id); e != nil {
		return backendstore.Detail{}, e
	}
	if detail, ok := s.details[id]; ok {
		return detail, nil
	}
	detail, e := s.connection.Detail(s.ctx, id)
	if e != nil {
		return detail, e
	}
	s.details[id] = detail
	return detail, nil
}
func (s *BackendQuerySnapshot) GetLogs(id string, limit int) ([]models.Log, error) {
	d, e := s.detail(id)
	if e != nil {
		return nil, e
	}
	logs := slices.Clone(d.Logs)
	slices.SortFunc(logs, func(a, b models.Log) int { return b.Timestamp.Compare(a.Timestamp) })
	if limit > 0 && len(logs) > limit {
		logs = logs[:limit]
	}
	return logs, nil
}
func (s *BackendQuerySnapshot) GetComments(id string) ([]models.Comment, error) {
	d, e := s.detail(id)
	if e != nil {
		return nil, e
	}
	out := make([]models.Comment, 0, len(d.Comments))
	for _, c := range d.Comments {
		out = append(out, models.Comment{ID: c.ID, IssueID: c.IssueID, SessionID: c.SessionID, Text: c.Text, CreatedAt: c.CreatedAt})
	}
	return out, nil
}
func (s *BackendQuerySnapshot) GetLatestHandoff(id string) (*models.Handoff, error) {
	d, e := s.detail(id)
	return d.LatestHandoff, e
}
func (s *BackendQuerySnapshot) GetLinkedFiles(id string) ([]models.IssueFile, error) {
	d, e := s.detail(id)
	return slices.Clone(d.Files), e
}
func (s *BackendQuerySnapshot) GetDependencies(id string) ([]string, error) {
	d, e := s.detail(id)
	if e != nil {
		return nil, e
	}
	out := make([]string, 0, len(d.Dependencies))
	for _, dep := range d.Dependencies {
		out = append(out, dep.DependsOnID)
	}
	return out, nil
}
func (s *BackendQuerySnapshot) policyIDs(expression string) (map[string]bool, error) {
	ids, e := s.connection.QueryIDs(s.ctx, expression)
	if e != nil {
		return nil, e
	}
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}
func (s *BackendQuerySnapshot) GetRejectedInProgressIssueIDs() (map[string]bool, error) {
	return s.policyIDs("rework()")
}
func (s *BackendQuerySnapshot) GetIssuesWithOpenDeps() (map[string]bool, error) {
	return s.policyIDs("has_open_deps()")
}

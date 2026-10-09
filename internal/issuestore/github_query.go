package issuestore

import (
	"cmp"
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/marcus/td/internal/db"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/query"
)

// GitHubQuerySnapshot adapts a complete issue listing to TDQ without opening
// SQLite. It is request-scoped, not a transaction: comments are fetched lazily
// with the request context, once per issue. Never share it between requests.
type GitHubQuerySnapshot struct {
	ctx      context.Context
	records  map[string]ghstore.Record
	activity ActivityStore
	cache    map[string][]models.Activity
}

var _ query.QuerySource = (*GitHubQuerySnapshot)(nil)

func NewGitHubQuerySnapshot(ctx context.Context, records []ghstore.Record, activity ActivityStore) (*GitHubQuerySnapshot, error) {
	s := &GitHubQuerySnapshot{ctx: ctx, records: map[string]ghstore.Record{}, activity: activity, cache: map[string][]models.Activity{}}
	for _, r := range records {
		if _, ok := s.records[r.ID]; ok {
			return nil, fmt.Errorf("GitHub listing repeated %s; retry the read", r.ID)
		}
		if r.DeletedAt == nil {
			s.records[r.ID] = r
		}
	}
	return s, nil
}
func (s *GitHubQuerySnapshot) GetIssue(id string) (*models.Issue, error) {
	if err := s.ctx.Err(); err != nil {
		return nil, err
	}
	n, err := ghstore.Number(id)
	if err != nil {
		return nil, err
	}
	r, ok := s.records[fmt.Sprintf("gh-%d", n)]
	if !ok {
		return nil, fmt.Errorf("issue %s not found", id)
	}
	return &r.Issue, nil
}
func (s *GitHubQuerySnapshot) ListIssues(o db.ListIssuesOptions) ([]models.Issue, error) {
	rest := o
	rest.SortBy = ""
	rest.SortDesc = false
	rest.Limit = 0
	if !reflect.DeepEqual(rest, db.ListIssuesOptions{}) {
		return nil, fmt.Errorf("query snapshot accepts only sorting and limit; apply predicates through TDQ")
	}
	if err := s.ctx.Err(); err != nil {
		return nil, err
	}
	issues := make([]models.Issue, 0, len(s.records))
	for _, r := range s.records {
		issues = append(issues, r.Issue)
	}
	if err := SortGitHubIssues(issues, o.SortBy, o.SortDesc); err != nil {
		return nil, err
	}
	if o.Limit > 0 && len(issues) > o.Limit {
		issues = issues[:o.Limit]
	}
	return issues, nil
}

// SortGitHubIssues uses the same field names as the HTTP and TDQ contracts.
func SortGitHubIssues(issues []models.Issue, field string, desc bool) error {
	if field == "" {
		field = "priority"
	}
	if mapped, ok := query.SortFieldToColumn[field]; ok {
		field = mapped
	}
	if !slices.Contains([]string{"priority", "id", "title", "status", "type", "points", "sprint", "created_at", "updated_at", "closed_at", "deleted_at", "due_date", "defer_until", "defer_count"}, field) {
		return fmt.Errorf("unsupported sort field %q", field)
	}
	date := func(t *time.Time) time.Time {
		if t == nil {
			return time.Time{}
		}
		return *t
	}
	calendar := func(s *string) string {
		if s == nil {
			return ""
		}
		return *s
	}
	slices.SortFunc(issues, func(a, b models.Issue) int {
		n := 0
		switch field {
		case "priority":
			n = strings.Compare(string(a.Priority), string(b.Priority))
		case "id":
			n = strings.Compare(a.ID, b.ID)
		case "title":
			n = strings.Compare(a.Title, b.Title)
		case "status":
			n = strings.Compare(string(a.Status), string(b.Status))
		case "type":
			n = strings.Compare(string(a.Type), string(b.Type))
		case "points":
			n = cmp.Compare(a.Points, b.Points)
		case "sprint":
			n = strings.Compare(a.Sprint, b.Sprint)
		case "created_at":
			n = a.CreatedAt.Compare(b.CreatedAt)
		case "updated_at":
			n = a.UpdatedAt.Compare(b.UpdatedAt)
		case "closed_at":
			n = date(a.ClosedAt).Compare(date(b.ClosedAt))
		case "deleted_at":
			n = date(a.DeletedAt).Compare(date(b.DeletedAt))
		case "due_date":
			n = strings.Compare(calendar(a.DueDate), calendar(b.DueDate))
		case "defer_until":
			n = strings.Compare(calendar(a.DeferUntil), calendar(b.DeferUntil))
		case "defer_count":
			n = cmp.Compare(a.DeferCount, b.DeferCount)
		}
		if desc {
			n = -n
		}
		if n != 0 {
			return n
		}
		return strings.Compare(a.ID, b.ID)
	})
	return nil
}
func (s *GitHubQuerySnapshot) activities(id string) ([]models.Activity, error) {
	issue, err := s.GetIssue(id)
	if err != nil {
		return nil, err
	}
	if a, ok := s.cache[issue.ID]; ok {
		return a, nil
	}
	if s.activity == nil {
		return nil, fmt.Errorf("GitHub activity reader unavailable")
	}
	a, err := s.activity.ListActivity(s.ctx, issue.ID)
	if err != nil {
		return nil, err
	}
	s.cache[issue.ID] = a
	return a, nil
}
func (s *GitHubQuerySnapshot) GetLogs(id string, limit int) ([]models.Log, error) {
	a, err := s.activities(id)
	if err != nil {
		return nil, err
	}
	out := []models.Log{}
	for _, v := range a {
		if v.Kind == "log" {
			out = append(out, models.Log{ID: v.ID, IssueID: v.IssueID, SessionID: v.SessionID, WorkSessionID: v.WorkSessionID, Message: v.Message, Type: v.LogType, Timestamp: v.CreatedAt})
		}
	}
	slices.SortFunc(out, func(a, b models.Log) int { return b.Timestamp.Compare(a.Timestamp) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (s *GitHubQuerySnapshot) GetComments(id string) ([]models.Comment, error) {
	a, err := s.activities(id)
	if err != nil {
		return nil, err
	}
	out := []models.Comment{}
	for _, v := range a {
		if v.Kind == "comment" {
			out = append(out, models.Comment{ID: v.ID, IssueID: v.IssueID, SessionID: v.SessionID, Text: v.Message, CreatedAt: v.CreatedAt})
		}
	}
	return out, nil
}
func (s *GitHubQuerySnapshot) GetLatestHandoff(id string) (*models.Handoff, error) {
	a, err := s.activities(id)
	if err != nil {
		return nil, err
	}
	var out *models.Handoff
	for _, v := range a {
		if v.Kind == "handoff" && (out == nil || !v.CreatedAt.Before(out.Timestamp)) {
			out = &models.Handoff{ID: v.ID, IssueID: v.IssueID, SessionID: v.SessionID, Done: v.Done, Remaining: v.Remaining, Decisions: v.Decisions, Uncertain: v.Uncertain, Timestamp: v.CreatedAt}
		}
	}
	return out, nil
}
func (s *GitHubQuerySnapshot) GetLinkedFiles(id string) ([]models.IssueFile, error) {
	issue, err := s.GetIssue(id)
	if err != nil {
		return nil, err
	}
	d := s.records[issue.ID].Details
	if d == nil {
		return []models.IssueFile{}, nil
	}
	return slices.Clone(d.Files), nil
}
func (s *GitHubQuerySnapshot) GetDependencies(id string) ([]string, error) {
	issue, err := s.GetIssue(id)
	if err != nil {
		return nil, err
	}
	d := s.records[issue.ID].Details
	if d == nil {
		return []string{}, nil
	}
	return slices.Clone(d.Dependencies), nil
}
func (s *GitHubQuerySnapshot) GetRejectedInProgressIssueIDs() (map[string]bool, error) {
	if err := s.ctx.Err(); err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for id, r := range s.records {
		if (r.Status != models.StatusOpen && r.Status != models.StatusInProgress) || r.Details == nil {
			continue
		}
		var rejected, reviewed time.Time
		for _, v := range r.Details.Transitions {
			if v.Action == "reject" && v.At.After(rejected) {
				rejected = v.At
			}
			if v.Action == "review" && v.At.After(reviewed) {
				reviewed = v.At
			}
		}
		if !rejected.IsZero() && !reviewed.After(rejected) {
			out[id] = true
		}
	}
	return out, nil
}
func (s *GitHubQuerySnapshot) GetIssuesWithOpenDeps() (map[string]bool, error) {
	if err := s.ctx.Err(); err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for id, r := range s.records {
		if r.Details != nil {
			for _, dep := range r.Details.Dependencies {
				target, ok := s.records[dep]
				if ok && target.Status != models.StatusClosed {
					out[id] = true
				}
			}
		}
	}
	return out, nil
}

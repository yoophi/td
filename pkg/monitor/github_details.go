package monitor

import (
	"context"
	"fmt"

	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/issuestore"
	"github.com/marcus/td/internal/models"
)

type MonitorDetailSource interface{ Details(string) IssueDetailsMsg }
type githubDetailReader interface {
	GitHubMonitorReader
	Get(context.Context, string) (*ghstore.Record, error)
	List(context.Context, bool) ([]ghstore.Record, error)
}

// Details reads a root plus a relationship listing. GitHub does not provide an
// atomic snapshot across those reads and issue comments.
func (s *GitHubDataSource) Details(id string) IssueDetailsMsg {
	fail := func(err error) IssueDetailsMsg { return IssueDetailsMsg{IssueID: id, Error: err} }
	n, err := ghstore.Number(id)
	if err != nil || id != fmt.Sprintf("gh-%d", n) {
		return fail(fmt.Errorf("monitor details require canonical gh-N issue ID"))
	}
	if err := s.ctx.Err(); err != nil {
		return fail(err)
	}
	if s.actor == "" {
		return fail(fmt.Errorf("GitHub monitor requires actual session"))
	}
	raw, err := s.open(s.ctx)
	if err != nil {
		return fail(err)
	}
	c, ok := raw.(githubDetailReader)
	if !ok {
		return fail(fmt.Errorf("GitHub monitor detail reader unavailable"))
	}
	root, err := c.Get(s.ctx, id)
	if err != nil {
		return fail(err)
	}
	if root == nil || root.ID != id || root.DeletedAt != nil {
		return fail(fmt.Errorf("detail target unavailable or deleted"))
	}
	records, err := c.List(s.ctx, true)
	if err != nil {
		return fail(err)
	}
	found := false
	for i, r := range records {
		if r.ID == id {
			records[i] = *root
			found = true
		}
	}
	if !found {
		records = append(records, *root)
	}
	snapshot, err := issuestore.NewGitHubQuerySnapshot(s.ctx, records, c)
	if err != nil {
		return fail(err)
	}
	msg := IssueDetailsMsg{IssueID: id, Issue: &root.Issue}
	msg.Logs, err = snapshot.GetLogs(id, 20)
	if err != nil {
		return fail(err)
	}
	msg.Comments, err = snapshot.GetComments(id)
	if err != nil {
		return fail(err)
	}
	msg.Handoff, err = snapshot.GetLatestHandoff(id)
	if err != nil {
		return fail(err)
	}
	deps, err := snapshot.GetDependencies(id)
	if err != nil {
		return fail(err)
	}
	for _, dep := range deps {
		target, err := snapshot.GetIssue(dep)
		if err != nil {
			return fail(fmt.Errorf("detail dependency %s: %w", dep, err))
		}
		msg.BlockedBy = append(msg.BlockedBy, *target)
	}
	if root.ParentID != "" {
		parent, err := snapshot.GetIssue(root.ParentID)
		if err != nil {
			return fail(fmt.Errorf("detail parent %s: %w", root.ParentID, err))
		}
		if parent.Type != models.TypeEpic {
			return fail(fmt.Errorf("detail parent %s is not an epic", parent.ID))
		}
		msg.ParentEpic = parent
	}
	for _, r := range records {
		if r.DeletedAt != nil {
			continue
		}
		if root.Type == models.TypeEpic && r.ParentID == id {
			msg.EpicTasks = append(msg.EpicTasks, r.Issue)
		}
		if r.Details != nil {
			for _, dep := range r.Details.Dependencies {
				if dep == id {
					msg.Blocks = append(msg.Blocks, r.Issue)
					break
				}
			}
		}
	}
	for _, issues := range [][]models.Issue{msg.Blocks, msg.EpicTasks} {
		if err := issuestore.SortGitHubIssues(issues, "priority", false); err != nil {
			return fail(err)
		}
	}
	if root.Details != nil {
		for _, r := range root.Details.Reviews {
			copy := r
			msg.Reviews = append(msg.Reviews, &copy)
		}
	}
	if root.Status == models.StatusInReview {
		facts, err := c.ObserveMonitorReview(s.ctx, root, s.actor)
		if err != nil {
			return fail(err)
		}
		if facts == nil {
			return fail(fmt.Errorf("missing detail review observation"))
		}
		msg.HasActiveApproval = facts.Fresh && facts.ActiveApproval
	}
	if err := s.ctx.Err(); err != nil {
		return fail(err)
	}
	observations := map[string]ghstore.Record{}
	for _, r := range records {
		observations[r.ID] = r
	}
	msg.Transitions = s.transitionHandles(observations)
	return msg
}

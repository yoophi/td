package serve

import (
	"context"
	"time"

	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/reviewpolicy"
	"github.com/marcus/td/pkg/monitor"
)

// GitHubContextSnapshot retains the complete observed history, including deleted
// tasks, alongside the same classified queues used by the HTTP monitor.
type GitHubContextSnapshot struct {
	Data       *monitor.RefreshDataMsg
	Records    []ghstore.Record
	Activities map[string][]models.Activity
}

type contextCapture struct {
	githubMonitorClient
	records    []ghstore.Record
	activities map[string][]models.Activity
}

func (c *contextCapture) ListIncludingDeleted(ctx context.Context, all bool) ([]ghstore.Record, error) {
	records, err := c.githubMonitorClient.ListIncludingDeleted(ctx, all)
	if err == nil {
		c.records = records
	}
	return records, err
}
func (c *contextCapture) ListActivityIncludingDeleted(ctx context.Context, id string) ([]models.Activity, error) {
	items, err := c.githubMonitorClient.ListActivityIncludingDeleted(ctx, id)
	if err == nil {
		c.activities[id] = items
	}
	return items, err
}

// ReadGitHubContext performs no writes and opens no SQLite database. The result
// is an observation, not an atomic snapshot or authorization for a mutation.
func ReadGitHubContext(ctx context.Context, client githubMonitorClient, actor string, mode reviewpolicy.Mode, focus *string) (*GitHubContextSnapshot, error) {
	c := &contextCapture{githubMonitorClient: client, activities: map[string][]models.Activity{}}
	data, err := fetchGitHubMonitor(ctx, c, actor, mode, focus, "", "", false, monitor.SortByPriority, time.Now())
	if err != nil {
		return nil, err
	}
	return &GitHubContextSnapshot{Data: data, Records: c.records, Activities: c.activities}, nil
}

package serve

import (
	"context"
	"fmt"
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

// bulkContextCapture forwards the optional bulk capability while retaining the
// same records/history used to classify the HTTP monitor queues.
type bulkContextCapture struct {
	*contextCapture
	provider ghstore.SnapshotReader
}

func (c *bulkContextCapture) ReadSnapshot(ctx context.Context, history bool) (*ghstore.Snapshot, error) {
	s, err := c.provider.ReadSnapshot(ctx, history)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, fmt.Errorf("GitHub context snapshot missing")
	}
	c.records = s.Issues(true)
	for _, r := range c.records {
		a, err := s.Activity(r.ID)
		if err != nil {
			return nil, err
		}
		c.activities[r.ID] = a
	}
	return s, nil
}

// ReadGitHubContext performs no writes and opens no SQLite database. The result
// is an observation, not an atomic snapshot or authorization for a mutation.
func ReadGitHubContext(ctx context.Context, client githubMonitorClient, actor string, mode reviewpolicy.Mode, focus *string) (*GitHubContextSnapshot, error) {
	c := &contextCapture{githubMonitorClient: client, activities: map[string][]models.Activity{}}
	var reader githubMonitorClient = c
	if bulk, ok := client.(ghstore.SnapshotReader); ok {
		reader = &bulkContextCapture{contextCapture: c, provider: bulk}
	}
	data, err := fetchGitHubMonitor(ctx, reader, actor, mode, focus, "", "", false, monitor.SortByPriority, time.Now())
	if err != nil {
		return nil, err
	}
	return &GitHubContextSnapshot{Data: data, Records: c.records, Activities: c.activities}, nil
}

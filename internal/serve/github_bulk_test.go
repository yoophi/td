package serve

import (
	"context"
	"errors"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/reviewpolicy"
	"github.com/marcus/td/pkg/monitor"
	"strings"
	"testing"
	"time"
)

type failedBulkMonitor struct{ githubMonitorClient }

func (*failedBulkMonitor) ReadSnapshot(context.Context, bool) (*ghstore.Snapshot, error) {
	return nil, errors.New("bulk observation failed")
}
func TestGitHubMonitorAndContextBulkFailureDoesNotPublishPartialData(t *testing.T) {
	c := &failedBulkMonitor{}
	data, err := fetchGitHubMonitor(context.Background(), c, "actor", reviewpolicy.ModeTrusted, nil, "", "auto", true, monitor.SortByPriority, time.Now())
	if data != nil || err == nil || !strings.Contains(err.Error(), "bulk observation failed") {
		t.Fatalf("%v %v", data, err)
	}
	observed, err := ReadGitHubContext(context.Background(), c, "actor", reviewpolicy.ModeTrusted, nil)
	if observed != nil || err == nil || !strings.Contains(err.Error(), "bulk observation failed") {
		t.Fatalf("%v %v", observed, err)
	}
}

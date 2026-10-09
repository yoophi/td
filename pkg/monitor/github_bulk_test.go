package monitor

import (
	"context"
	"errors"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/reviewpolicy"
	"strings"
	"testing"
	"time"
)

type failedBulkReader struct{ GitHubMonitorReader }

func (*failedBulkReader) ReadSnapshot(context.Context, bool) (*ghstore.Snapshot, error) {
	return nil, errors.New("bulk observation failed")
}
func TestGitHubMonitorBulkFailureDoesNotFallBackToIndividualReads(t *testing.T) {
	_, err := FetchGitHubData(context.Background(), &failedBulkReader{}, "actor", reviewpolicy.ModeTrusted, nil, "", "auto", true, SortByPriority, time.Now())
	if err == nil || !strings.Contains(err.Error(), "bulk observation failed") {
		t.Fatalf("%v", err)
	}
	s := &GitHubDataSource{ctx: context.Background(), open: func(context.Context) (GitHubMonitorReader, error) { return &failedBulkReader{}, nil }}
	result := s.Handoffs()
	if result.Error == nil || !strings.Contains(result.Error.Error(), "bulk observation failed") {
		t.Fatalf("%v", result.Error)
	}
}

package monitor

import (
	"context"
	"strings"
	"testing"

	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/internal/models"
)

func TestBackendMonitorDeleteRequiresOriginalRevision(t *testing.T) {
	row := backendstore.Issue{Issue: models.Issue{ID: "td-0123456789abcdef", Title: "Delete me", Status: models.StatusOpen}, Revision: "r-1"}
	fixture := &backendWorkflowFixture{observed: row, current: row}
	source := NewBackendDataSource(context.Background(), fixture, func(context.Context) error { return nil })
	issue, writer, err := source.PrepareDelete(row.ID)
	if err != nil || issue == nil || issue.ID != row.ID || writer == nil {
		t.Fatalf("delete preparation failed: %+v, %v", issue, err)
	}
	fixture.current.Revision = "r-2"
	if err := writer.Delete(); err == nil || !strings.Contains(err.Error(), "changed since") || fixture.writes != 0 {
		t.Fatalf("stale issue deletion accepted: %v", err)
	}
	fixture.current.Revision = "r-1"
	if err := writer.Delete(); err != nil || fixture.request.Method != "DELETE" || fixture.request.Resource != "/issues/"+row.ID || fixture.request.Revision != "r-1" || fixture.writes != 1 {
		t.Fatalf("observed deletion failed: %+v, %v", fixture.request, err)
	}
}

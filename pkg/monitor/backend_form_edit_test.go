package monitor

import (
	"context"
	"strings"
	"testing"

	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/internal/models"
)

func TestBackendMonitorPrepareEditUsesCurrentDetail(t *testing.T) {
	root := backendstore.Issue{Issue: models.Issue{ID: "td-0123456789abcdef", Title: "Current"}, Revision: "r-2"}
	dep := "td-bbbbbbbbbbbbbbbb"
	fixture := backendDetailFixture{detail: backendstore.Detail{Issue: root, Dependencies: []models.IssueDependency{{IssueID: root.ID, DependsOnID: dep}}}}
	guardCalls := 0
	source := NewBackendDataSource(context.Background(), fixture, func(context.Context) error { guardCalls++; return nil })
	handle := &backendIssueTransition{source: source, observed: root}
	issue, deps, err := handle.PrepareEdit()
	if err != nil || issue.Title != "Current" || len(deps) != 1 || deps[0] != dep || guardCalls != 2 {
		t.Fatalf("edit observation mismatch: issue=%+v deps=%v guard=%d err=%v", issue, deps, guardCalls, err)
	}
	fixture.detail.Issue.Revision = "r-3"
	source = NewBackendDataSource(context.Background(), fixture, func(context.Context) error { return nil })
	handle.source = source
	if _, _, err := handle.PrepareEdit(); err == nil || !strings.Contains(err.Error(), "changed since") {
		t.Fatalf("stale edit accepted: %v", err)
	}
	fixture.detail.Issue = root
	fixture.detail.Dependencies[0].IssueID = dep
	handle.source = NewBackendDataSource(context.Background(), fixture, func(context.Context) error { return nil })
	if _, _, err := handle.PrepareEdit(); err == nil || !strings.Contains(err.Error(), "dependency") {
		t.Fatalf("invalid dependency accepted: %v", err)
	}
}

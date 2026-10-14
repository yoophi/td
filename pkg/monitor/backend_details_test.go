package monitor

import (
	"context"
	"testing"

	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/internal/models"
)

type backendDetailFixture struct {
	detail backendstore.Detail
	rows   []backendstore.Issue
}

func (f backendDetailFixture) Monitor(context.Context, string, bool, string) (backendstore.Monitor, error) {
	return backendstore.Monitor{}, nil
}
func (f backendDetailFixture) Detail(context.Context, string) (backendstore.Detail, error) {
	return f.detail, nil
}
func (f backendDetailFixture) List(context.Context, bool) ([]backendstore.Issue, error) {
	return f.rows, nil
}

func TestBackendMonitorDetailsRequireMatchingObservation(t *testing.T) {
	root := backendstore.Issue{Issue: models.Issue{ID: "td-0123456789abcdef", ParentID: "td-aaaaaaaaaaaaaaaa"}, Revision: "r-2"}
	parent := backendstore.Issue{Issue: models.Issue{ID: "td-aaaaaaaaaaaaaaaa", Type: models.TypeEpic}, Revision: "r-1"}
	dep := backendstore.Issue{Issue: models.Issue{ID: "td-bbbbbbbbbbbbbbbb"}, Revision: "r-1"}
	fixture := backendDetailFixture{detail: backendstore.Detail{Issue: root, Dependencies: []models.IssueDependency{{IssueID: root.ID, DependsOnID: dep.ID}}}, rows: []backendstore.Issue{root, parent, dep}}
	source := NewBackendDataSource(context.Background(), fixture, func(context.Context) error { return nil })
	if msg := source.Details(root.ID); msg.Error != nil || msg.ParentEpic == nil || msg.ParentEpic.ID != parent.ID || len(msg.BlockedBy) != 1 || msg.BlockedBy[0].ID != dep.ID {
		t.Fatalf("valid detail omitted: %+v", msg)
	}
	fixture.rows[0].Revision = "r-3"
	source = NewBackendDataSource(context.Background(), fixture, func(context.Context) error { return nil })
	if msg := source.Details(root.ID); msg.Error == nil || msg.Issue != nil {
		t.Fatalf("mixed-revision detail was accepted: %+v", msg)
	}
}

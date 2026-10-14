package monitor

import (
	"context"
	"testing"
	"time"

	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/internal/models"
)

type backendDetailFixture struct {
	detail        backendstore.Detail
	detailErr     error
	rows          []backendstore.Issue
	archiveDetail backendstore.Detail
	archiveRows   []backendstore.Issue
}

func (f backendDetailFixture) Monitor(context.Context, string, bool, string) (backendstore.Monitor, error) {
	return backendstore.Monitor{}, nil
}
func (f backendDetailFixture) Detail(context.Context, string) (backendstore.Detail, error) {
	return f.detail, f.detailErr
}
func (f backendDetailFixture) List(context.Context, bool) ([]backendstore.Issue, error) {
	return f.rows, nil
}
func (f backendDetailFixture) DetailIncludingDeleted(context.Context, string) (backendstore.Detail, error) {
	return f.archiveDetail, nil
}
func (f backendDetailFixture) ListIssuesIncludingDeleted(context.Context) ([]backendstore.Issue, error) {
	return f.archiveRows, nil
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

func TestBackendMonitorDetailsOpenArchivedHandoffIssue(t *testing.T) {
	id := "td-0123456789abcdef"
	when := time.Now().UTC()
	deleted := backendstore.Issue{Issue: models.Issue{ID: id, Title: "Archived", DeletedAt: &when}, Revision: "r-3"}
	fixture := backendDetailFixture{detailErr: &backendstore.APIError{StatusCode: 404}, archiveDetail: backendstore.Detail{Issue: deleted}, archiveRows: []backendstore.Issue{deleted}}
	source := NewBackendDataSource(context.Background(), fixture, func(context.Context) error { return nil })
	msg := source.Details(id)
	if msg.Error != nil || msg.Issue == nil || msg.Issue.Title != "Archived" || len(msg.Transitions) != 0 {
		t.Fatalf("archived issue modal unavailable: %+v", msg)
	}
}

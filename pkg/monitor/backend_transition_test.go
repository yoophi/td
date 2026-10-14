package monitor

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/internal/models"
)

type backendWorkflowFixture struct {
	observed backendstore.Issue
	current  backendstore.Issue
	request  backendstore.MutationRequest
	writes   int
}

func (f *backendWorkflowFixture) Monitor(context.Context, string, bool, string) (backendstore.Monitor, error) {
	return backendstore.Monitor{}, nil
}
func (f *backendWorkflowFixture) Detail(context.Context, string) (backendstore.Detail, error) {
	return backendstore.Detail{Issue: f.current}, nil
}
func (f *backendWorkflowFixture) RunIntent(_ context.Context, _ any, prepare func() (backendstore.MutationRequest, error), out any, verify func() error) error {
	var err error
	f.request, err = prepare()
	if err != nil {
		return err
	}
	f.writes++
	result := f.observed
	result.Status = models.StatusInReview
	if strings.HasSuffix(f.request.Resource, "/reopen") {
		result.Status = models.StatusOpen
	}
	result.Revision = "r-2"
	data, _ := json.Marshal(map[string]any{"issue": result})
	if err := json.Unmarshal(data, out); err != nil {
		return err
	}
	return verify()
}

func TestBackendMonitorReopenUsesWorkflowEndpoint(t *testing.T) {
	row := backendstore.Issue{Issue: models.Issue{ID: "td-0123456789abcdef", Status: models.StatusClosed}, Revision: "r-1"}
	fixture := &backendWorkflowFixture{observed: row, current: row}
	source := NewBackendDataSource(context.Background(), fixture, func(context.Context) error { return nil })
	handle := source.backendTransitionHandlesForRows([]backendstore.Issue{row})[row.ID]
	if err := handle.Transition("reopen"); err != nil || fixture.request.Resource != "/issues/"+row.ID+"/reopen" {
		t.Fatalf("reopen did not use server workflow: %+v, %v", fixture.request, err)
	}
}

func TestBackendMonitorReviewUsesObservedWorkflow(t *testing.T) {
	row := backendstore.Issue{Issue: models.Issue{ID: "td-0123456789abcdef", Status: models.StatusInProgress}, Revision: "r-1"}
	fixture := &backendWorkflowFixture{observed: row, current: row}
	source := NewBackendDataSource(context.Background(), fixture, func(context.Context) error { return nil })
	handle := source.backendTransitionHandlesForRows([]backendstore.Issue{row})[row.ID]
	if handle == nil {
		t.Fatal("backend workflow handle unavailable")
	}
	if err := handle.Transition("review"); err != nil || fixture.request.Resource != "/issues/"+row.ID+"/review" || fixture.request.Revision != "r-1" || fixture.writes != 1 {
		t.Fatalf("observed review failed: %+v, %v", fixture.request, err)
	}
	fixture.current.Revision = "r-2"
	if err := handle.Transition("review"); err == nil || !strings.Contains(err.Error(), "changed since") || fixture.writes != 1 {
		t.Fatalf("stale review accepted: %v", err)
	}
	if err := handle.Transition("close"); err == nil || fixture.writes != 1 {
		t.Fatalf("unsupported transition accepted: %v", err)
	}
}

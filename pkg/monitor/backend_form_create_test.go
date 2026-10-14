package monitor

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/internal/models"
)

type backendCreateFixture struct {
	request backendstore.MutationRequest
	writes  int
}

func (f *backendCreateFixture) Monitor(context.Context, string, bool, string) (backendstore.Monitor, error) {
	return backendstore.Monitor{}, nil
}
func (f *backendCreateFixture) RunIntent(_ context.Context, _ any, prepare func() (backendstore.MutationRequest, error), out any, verify func() error) error {
	var err error
	f.request, err = prepare()
	if err != nil {
		return err
	}
	f.writes++
	data, _ := json.Marshal(map[string]any{"issue": backendstore.Issue{Issue: models.Issue{ID: "td-0123456789abcdef", Status: models.StatusOpen}, Revision: "r-1"}})
	if err := json.Unmarshal(data, out); err != nil {
		return err
	}
	return verify()
}

func TestBackendMonitorFormCreateUsesAtomicDependencies(t *testing.T) {
	fixture := &backendCreateFixture{}
	source := NewBackendDataSource(context.Background(), fixture, func(context.Context) error { return nil })
	dep := "td-aaaaaaaaaaaaaaaa"
	issue := models.Issue{Title: "New monitor issue", Type: models.TypeTask, Priority: models.PriorityP2, Description: "Description", Labels: []string{"monitor"}}
	id, err := source.CreateIssue(issue, []string{dep})
	if err != nil || id != "td-0123456789abcdef" || fixture.request.Method != "POST" || fixture.request.Resource != "/issues" || fixture.writes != 1 {
		t.Fatalf("backend form creation failed: %q, %+v, %v", id, fixture.request, err)
	}
	var body struct {
		Description string   `json:"description"`
		DependsOn   []string `json:"depends_on"`
	}
	if err := json.Unmarshal(fixture.request.Body, &body); err != nil || body.Description != "Description" || len(body.DependsOn) != 1 || body.DependsOn[0] != dep {
		t.Fatalf("atomic create payload lost fields or dependencies: %+v, %v", body, err)
	}
	if _, err := source.CreateIssue(issue, []string{dep, dep}); err == nil || fixture.writes != 1 {
		t.Fatalf("duplicate dependency was submitted: %v", err)
	}
}

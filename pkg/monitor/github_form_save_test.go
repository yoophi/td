package monitor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
)

type formSaveFixture struct {
	formEditFixture
	root    *ghstore.Record
	changes []ghstore.Changes
	options ghstore.TransitionOptions
	calls   []string
	failAt  string
}

func (f *formSaveFixture) ReadObserved(_ context.Context, r *ghstore.Record) (*ghstore.Record, error) {
	if r.ID != f.root.ID {
		return nil, errors.New("wrong observation")
	}
	f.calls = append(f.calls, "read")
	return f.root, f.readErr
}
func (f *formSaveFixture) UpdateWorkflowObserved(_ context.Context, r *ghstore.Record, change ghstore.Changes, options ghstore.TransitionOptions) (*ghstore.Record, error) {
	if r != f.root {
		return nil, errors.New("revision was not carried forward")
	}
	action := "fields"
	if change.Status != nil {
		action = "status"
	}
	f.calls = append(f.calls, action)
	if f.failAt == action {
		return nil, &ghstore.ConflictError{ID: r.ID}
	}
	f.changes = append(f.changes, change)
	f.options = options
	next := *r
	if change.Title != nil {
		next.Title = *change.Title
	}
	if change.Status != nil {
		next.Status = *change.Status
	}
	f.root = &next
	return f.root, nil
}
func (f *formSaveFixture) ChangeDependencyObserved(_ context.Context, r *ghstore.Record, id string, add bool, actor string) (*ghstore.Record, error) {
	if r != f.root || actor != "actual-monitor" {
		return nil, errors.New("wrong revision or actor")
	}
	action := "remove:" + id
	if add {
		action = "add:" + id
	}
	f.calls = append(f.calls, action)
	if f.failAt == action {
		return nil, &ghstore.ConflictError{ID: r.ID}
	}
	next := *r
	f.root = &next
	return f.root, nil
}
func newFormSaveFixture(t *testing.T) (*formSaveFixture, *githubIssueTransition) {
	t.Helper()
	r := ghstore.Record{Issue: models.Issue{ID: "gh-1", Title: "old", Type: models.TypeTask, Priority: models.PriorityP2, Status: models.StatusOpen}, Details: &ghstore.IssueDetails{Dependencies: []string{"gh-2"}}}
	f := &formSaveFixture{root: &r}
	s := &GitHubDataSource{ctx: context.Background(), baseDir: t.TempDir(), actor: "actual-monitor", open: func(context.Context) (GitHubMonitorReader, error) { return f, nil }}
	return f, &githubIssueTransition{source: s, observed: r}
}
func TestGitHubFormSaveCarriesRevisionAcrossFieldsDependenciesAndStatus(t *testing.T) {
	f, store := newFormSaveFixture(t)
	issue := store.ObservedIssue()
	issue.Title = "edited"
	issue.Status = models.StatusInProgress
	if err := store.SaveEdit(issue, []string{"3"}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.calls, ",") != "read,fields,remove:gh-2,add:gh-3,status" || f.options.SessionID != "actual-monitor" || f.options.AgentType != "monitor" || f.options.SelfReview || f.options.ReviewedBy != "" {
		t.Fatalf("%v %+v", f.calls, f.options)
	}
	if f.changes[0].Title == nil || f.changes[0].Description != nil || f.changes[0].Details != nil || f.changes[1].Status == nil {
		t.Fatal("changed untouched fields or workflow details")
	}
}
func TestGitHubFormSavePartialFailureIsExplicitAndStops(t *testing.T) {
	for _, step := range []string{"add:gh-3", "status"} {
		t.Run(step, func(t *testing.T) {
			f, store := newFormSaveFixture(t)
			f.failAt = step
			issue := store.ObservedIssue()
			issue.Title = "saved fields"
			issue.Status = models.StatusInProgress
			err := store.SaveEdit(issue, []string{"gh-3"})
			var conflict *ghstore.ConflictError
			if !errors.As(err, &conflict) || !strings.Contains(err.Error(), "some form changes were saved") || f.root.Title != issue.Title || f.root.Status != models.StatusOpen {
				t.Fatalf("%v %v", f.calls, err)
			}
			if f.calls[len(f.calls)-1] != step {
				t.Fatal("continued after failure")
			}
		})
	}
	f, store := newFormSaveFixture(t)
	f.readErr = &ghstore.ConflictError{ID: "gh-1"}
	if err := store.SaveEdit(store.ObservedIssue(), []string{"gh-2"}); err == nil || len(f.changes) != 0 || len(f.calls) != 1 {
		t.Fatal("no-op form bypassed conflict check")
	}
}
func TestGitHubFormSaveRejectsInvalidInputBeforeOpening(t *testing.T) {
	for _, deps := range [][]string{{"gh-1"}, {"gh-2", "2"}, {"not-an-id"}} {
		f, store := newFormSaveFixture(t)
		if err := store.SaveEdit(store.ObservedIssue(), deps); err == nil || len(f.calls) != 0 {
			t.Fatalf("%v %v", deps, err)
		}
	}
}
func TestGitHubFormSaveUIKeepsDraftOnErrorAndClosesOnlyAfterSuccess(t *testing.T) {
	for _, failed := range []bool{false, true} {
		f, store := newFormSaveFixture(t)
		m := Model{DataSource: store.source, Width: 100, Height: 40, FormOpen: true, FormEditStore: store, FormAutofillRequest: 7, FormState: newFormStateForEditWithTheme(&store.observed.Issue, Theme{})}
		m.FormState.Title = "draft"
		m.FormState.Dependencies = "gh-3"
		if failed {
			f.failAt = "add:gh-3"
		}
		pending, cmd := m.submitForm()
		m = pending.(Model)
		if cmd == nil || !m.WorkflowPending || !m.PendingRemoteWrite() || !m.FormOpen {
			t.Fatal("submission mutated optimistic display")
		}
		if _, duplicate := m.submitForm(); duplicate != nil {
			t.Fatal("duplicate submit queued")
		}
		msg := cmd().(MonitorFormSavedMsg)
		updated, _ := m.Update(msg)
		m = updated.(Model)
		if m.WorkflowPending || m.FormEditStore != nil {
			t.Fatal("submission handle retained")
		}
		if failed {
			if !m.FormOpen || m.FormState.Title != "draft" || m.FormSaveError == nil || !m.StatusIsError {
				t.Fatal("failure erased draft/error")
			}
			late, _ := m.handleFormUpdate(AutofillResultMsg{Request: m.FormAutofillRequest})
			m = late.(Model)
			if m.FormSaveError == nil || !strings.Contains(m.renderFormModal(), "some form changes were saved") {
				t.Fatal("late autocomplete erased save error")
			}
			if _, retry := m.submitForm(); retry != nil {
				t.Fatal("uncertain submission repeated")
			}
		} else if m.FormOpen {
			t.Fatal("successful submission left form open")
		}
	}
}

package monitor

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
)

type formSaveFixture struct {
	formEditFixture
	root      *ghstore.Record
	changes   []ghstore.Changes
	relations ghstore.RelationChanges
	options   ghstore.TransitionOptions
	calls     []string
	failAt    string
}

func (f *formSaveFixture) ReadObserved(_ context.Context, r *ghstore.Record) (*ghstore.Record, error) {
	if r.ID != f.root.ID {
		return nil, errors.New("wrong observation")
	}
	f.calls = append(f.calls, "read")
	return f.root, f.readErr
}
func (f *formSaveFixture) UpdateCommandObserved(_ context.Context, r *ghstore.Record, change ghstore.Changes, relations ghstore.RelationChanges, options ghstore.TransitionOptions) (*ghstore.Record, error) {
	if r != f.root {
		return nil, errors.New("original observation was not carried into command")
	}
	f.calls = append(f.calls, "command")
	f.changes = append(f.changes, change)
	f.options, f.relations = options, relations
	if f.failAt != "" {
		return nil, fmt.Errorf("workflow group ghc-100 stopped; some form changes were saved; inspect and reconcile without replay: %w", &ghstore.ConflictError{ID: r.ID})
	}
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
	if strings.Join(f.calls, ",") != "read,command" || f.options.SessionID != "actual-monitor" || f.options.AgentType != "monitor" || f.options.SelfReview || f.options.ReviewedBy != "" {
		t.Fatalf("%v %+v", f.calls, f.options)
	}
	if f.changes[0].Title == nil || f.changes[0].Description != nil || f.changes[0].Details != nil || f.changes[0].Status == nil || f.relations.DependsOn == nil || !slices.Equal(*f.relations.DependsOn, []string{"gh-3"}) {
		t.Fatal("changed untouched fields or workflow details")
	}
}
func TestGitHubFormSavePartialFailureIsExplicitAndStops(t *testing.T) {
	f, store := newFormSaveFixture(t)
	f.failAt = "command"
	issue := store.ObservedIssue()
	issue.Title = "requested fields"
	issue.Status = models.StatusInProgress
	err := store.SaveEdit(issue, []string{"gh-3"})
	var conflict *ghstore.ConflictError
	if !errors.As(err, &conflict) || !strings.Contains(err.Error(), "ghc-100") || !strings.Contains(err.Error(), "without replay") || strings.Join(f.calls, ",") != "read,command" {
		t.Fatalf("%v %v", f.calls, err)
	}
	f, store = newFormSaveFixture(t)
	f.readErr = &ghstore.ConflictError{ID: "gh-1"}
	if err := store.SaveEdit(store.ObservedIssue(), []string{"gh-2"}); err == nil || len(f.changes) != 0 || len(f.calls) != 1 {
		t.Fatal("no-op form bypassed conflict check")
	}
}
func TestGitHubFormSaveUnchangedReviewStatusAndNoop(t *testing.T) {
	f, store := newFormSaveFixture(t)
	f.root.Status, store.observed.Status = models.StatusInReview, models.StatusInReview
	issue := store.ObservedIssue()
	if err := store.SaveEdit(issue, []string{"gh-2"}); err != nil || strings.Join(f.calls, ",") != "read" {
		t.Fatal("no-op form wrote", err, f.calls)
	}
	issue.Title = "field-only change"
	if err := store.SaveEdit(issue, []string{"gh-2"}); err != nil || len(f.changes) != 1 || f.changes[0].Status != nil {
		t.Fatal("field edit resubmitted review", err, f.changes)
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
			f.failAt = "command"
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
			if m.FormSaveError == nil || !strings.Contains(m.FormSaveError.Error(), "some form changes were saved") || !strings.Contains(m.renderFormModal(), "ghc-100") {
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

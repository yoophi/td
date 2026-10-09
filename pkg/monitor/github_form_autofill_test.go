package monitor

import (
	tea "charm.land/bubbletea/v2"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
)

func TestGitHubFormAutofillFiltersBeforeLimit(t *testing.T) {
	now := time.Now()
	f := &monitorDataFixture{}
	for i := 1; i <= 505; i++ {
		f.records = append(f.records, ghstore.Record{Issue: models.Issue{ID: fmt.Sprintf("gh-%d", i), Title: "candidate", Status: models.StatusOpen, Priority: models.PriorityP2}})
	}
	f.records = append(f.records, ghstore.Record{Issue: models.Issue{ID: "gh-600", Type: models.TypeEpic, Status: models.StatusInReview, Priority: models.PriorityP0}}, ghstore.Record{Issue: models.Issue{ID: "gh-601", Status: models.StatusClosed, Priority: models.PriorityP0}}, ghstore.Record{Issue: models.Issue{ID: "gh-602", Status: models.StatusOpen, Priority: models.PriorityP0, DeletedAt: &now}})
	s := &GitHubDataSource{ctx: context.Background(), open: func(context.Context) (GitHubMonitorReader, error) { return f, nil }}
	result := s.Autofill()
	if result.Error != nil || len(result.Items) != 500 || result.Items[0].ID != "gh-600" {
		t.Fatalf("%+v", result)
	}
	for _, item := range result.Items {
		if item.ID == "gh-601" || item.ID == "gh-602" {
			t.Fatal("invisible task suggested")
		}
	}
	f.records = append(f.records, f.records[0])
	if r := s.Autofill(); r.Error == nil || len(r.Items) != 0 {
		t.Fatal("partial duplicate listing exposed")
	}
	f.fail = errors.New("rate limited")
	if r := s.Autofill(); !errors.Is(r.Error, f.fail) {
		t.Fatalf("%+v", r)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.ctx = ctx
	if r := s.Autofill(); !errors.Is(r.Error, context.Canceled) {
		t.Fatalf("%+v", r)
	}
}

func TestGitHubFormAutofillKeepsDraftAndRejectsLateReplies(t *testing.T) {
	f := &monitorDataFixture{boardSourceFixture: boardSourceFixture{records: []ghstore.Record{{Issue: models.Issue{ID: "gh-1", Type: models.TypeEpic, Status: models.StatusOpen}}}}}
	s := &GitHubDataSource{ctx: context.Background(), open: func(context.Context) (GitHubMonitorReader, error) { return f, nil }}
	m := Model{DataSource: s, Width: 100, Height: 40}
	opened, initial := m.openNewIssueForm()
	m = opened.(Model)
	if m.FormAutofillRequest != 1 {
		t.Fatal("opening form did not retain request generation")
	}
	m.FormState.Title = "unsaved draft"
	var reply AutofillResultMsg
	for _, cmd := range initial().(tea.BatchMsg) {
		// The second command is the data read; the first initializes huh.
		if result, ok := cmd().(AutofillResultMsg); ok {
			reply = result
		}
	}
	if reply.Request != m.FormAutofillRequest || len(reply.Items) != 1 {
		t.Fatal("initial reply mismatched form")
	}
	updated, _ := m.handleFormUpdate(reply)
	m = updated.(Model)
	if len(m.FormState.AutofillEpics) != 1 || m.FormState.Title != "unsaved draft" {
		t.Fatal("candidate load changed draft")
	}
	failure := reply
	failure.Error = errors.New("permission denied")
	failure.Items = nil
	updated, _ = m.handleFormUpdate(failure)
	m = updated.(Model)
	if !strings.Contains(m.renderFormModal(), "permission denied") {
		t.Fatal("form error is not visible")
	}
	if !m.StatusIsError || len(m.FormState.AutofillAll) != 1 || m.FormState.Title != "unsaved draft" {
		t.Fatal("failure erased form")
	}
	m.closeForm()
	opened, _ = m.openNewIssueForm()
	m = opened.(Model)
	updated, _ = m.handleFormUpdate(reply)
	m = updated.(Model)
	if len(m.FormState.AutofillAll) != 0 {
		t.Fatal("old form response replaced new candidates")
	}
}

type noAutofillSource struct{}

func (noAutofillSource) Fetch(string, bool, SortMode) RefreshDataMsg { return RefreshDataMsg{} }
func TestGitHubFormAutofillMissingReaderDoesNotFallBackToSQLite(t *testing.T) {
	m := Model{DataSource: noAutofillSource{}}
	result := m.loadFormAutofill()().(AutofillResultMsg)
	if result.Error == nil || !strings.Contains(result.Error.Error(), "reader unavailable") {
		t.Fatalf("%+v", result)
	}
}

package monitor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/internal/models"
)

func TestBackendMonitorAutofillExcludesClosedAndArchivedBeforeOrdering(t *testing.T) {
	now := time.Now().UTC()
	rows := []backendstore.Issue{
		{Issue: models.Issue{ID: "td-aaaaaaaaaaaaaaaa", Title: "Closed", Status: models.StatusClosed, Type: models.TypeTask, Priority: models.PriorityP0}},
		{Issue: models.Issue{ID: "td-bbbbbbbbbbbbbbbb", Title: "Open", Status: models.StatusOpen, Type: models.TypeTask, Priority: models.PriorityP2, UpdatedAt: now}},
		{Issue: models.Issue{ID: "td-cccccccccccccccc", Title: "Urgent", Status: models.StatusInProgress, Type: models.TypeEpic, Priority: models.PriorityP0, UpdatedAt: now}},
		{Issue: models.Issue{ID: "td-dddddddddddddddd", Title: "Archived", Status: models.StatusOpen, Type: models.TypeTask, Priority: models.PriorityP0, DeletedAt: &now}},
	}
	fixture := backendDetailFixture{rows: rows}
	source := NewBackendDataSource(context.Background(), fixture, func(context.Context) error { return nil })
	result := source.Autofill()
	if result.Error != nil || len(result.Items) != 2 || result.Items[0].Title != "Urgent" || result.Items[0].Type != models.TypeEpic || result.Items[1].Title != "Open" {
		t.Fatalf("autocomplete candidates differ from backend state: %+v", result)
	}
	stale := errors.New("context changed")
	checks := 0
	source = NewBackendDataSource(context.Background(), fixture, func(context.Context) error {
		checks++
		if checks == 2 {
			return stale
		}
		return nil
	})
	if result := source.Autofill(); !errors.Is(result.Error, stale) || len(result.Items) != 0 {
		t.Fatalf("stale autocomplete candidates published: %+v", result)
	}
}

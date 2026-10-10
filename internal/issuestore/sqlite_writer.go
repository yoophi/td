package issuestore

import (
	"context"
	"fmt"
	"reflect"

	"github.com/marcus/td/internal/db"
	"github.com/marcus/td/internal/models"
)

// NewSQLiteWriter borrows an already-selected database; it neither opens a
// database nor selects a fallback store. The owner remains responsible for Close.
// SQLite's logged writes and observed comparison stay inside their DB transaction.
func NewSQLiteWriter(database *db.DB, sessionID string) IssueWriter[models.Issue, models.Issue] {
	return &sqliteIssueWriter{database: database, sessionID: sessionID}
}

type sqliteIssueWriter struct {
	database  *db.DB
	sessionID string
}

func (w *sqliteIssueWriter) ready(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if w.database == nil {
		return fmt.Errorf("SQLite issue writer has no database")
	}
	return nil
}

func (w *sqliteIssueWriter) Get(ctx context.Context, id string) (*models.Issue, error) {
	if err := w.ready(ctx); err != nil {
		return nil, err
	}
	issue, err := w.database.GetIssue(id)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return issue, nil
}

func (w *sqliteIssueWriter) Create(ctx context.Context, issue *models.Issue) (*models.Issue, error) {
	if err := w.ready(ctx); err != nil {
		return nil, err
	}
	if issue == nil {
		return nil, fmt.Errorf("issue is required")
	}
	if issue.Status != "" && issue.Status != models.StatusOpen {
		return nil, fmt.Errorf("issue creation requires open status; use a workflow transition")
	}
	created := *issue
	if err := w.database.CreateIssueLogged(&created, w.sessionID); err != nil {
		return nil, err
	}
	return &created, nil
}

// UpdateObserved receives a validated field-edit draft and its original snapshot.
// It refuses changes to lifecycle/attribution/read-only fields: those require the
// dedicated workflow contract. Cancellation is checked before the transaction;
// an entered SQLite transaction retains the existing DB completion semantics.
func (w *sqliteIssueWriter) UpdateObserved(ctx context.Context, observed *models.Issue, change models.Issue) (*models.Issue, error) {
	if err := w.ready(ctx); err != nil {
		return nil, err
	}
	if observed == nil || observed.ID == "" || change.ID != observed.ID {
		return nil, fmt.Errorf("issue update requires the original observation of the same issue")
	}
	unchanged := change
	unchanged.Title = observed.Title
	unchanged.Description = observed.Description
	unchanged.Type = observed.Type
	unchanged.Priority = observed.Priority
	unchanged.Points = observed.Points
	unchanged.Labels = observed.Labels
	unchanged.ParentID = observed.ParentID
	unchanged.Acceptance = observed.Acceptance
	unchanged.Sprint = observed.Sprint
	unchanged.Minor = observed.Minor
	unchanged.DeferUntil = observed.DeferUntil
	unchanged.DueDate = observed.DueDate
	if !reflect.DeepEqual(unchanged, *observed) {
		return nil, fmt.Errorf("field updates cannot change lifecycle, attribution or read-only fields; use the dedicated workflow")
	}
	if err := w.database.UpdateIssueLoggedIfUnchanged(&change, observed, w.sessionID, models.ActionUpdate); err != nil {
		return nil, err
	}
	return &change, nil
}

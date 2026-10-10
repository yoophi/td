package issuestore

import (
	"context"

	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
)

// IssueWriter is the minimal observed CRUD contract. R retains the backend's
// observation, including its revision evidence; C retains its validated field
// change type. A caller must not reconstruct R from an issue DTO and claim it
// was observed, or assume that Create and UpdateObserved form a transaction.
//
// Workflow transitions, reviews, relations and history have their own contracts.
// In particular this interface must not replace SQLite workflow transactions
// with a Get followed by a generic field update. No SQL handle crosses it.
// Cancellation or a failed remote write can leave an uncertain result: inspect
// the store's current state and journal before retrying, never replay implicitly.
type IssueWriter[R, C any] interface {
	Get(context.Context, string) (*R, error)
	Create(context.Context, *models.Issue) (*R, error)
	UpdateObserved(context.Context, *R, C) (*R, error)
}

var _ IssueWriter[ghstore.Record, ghstore.Changes] = (*ghstore.Client)(nil)

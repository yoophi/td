package issuestore

import (
	"context"

	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
)

// ActivityStore reads and appends shared issue history. Append is a single
// backend write, not a transaction with an issue update. Callers must surface
// uncertain results and must not automatically retry writes. OperationID lets
// a caller inspect remote history after a lost response; it is not a lock or
// an idempotency guarantee. Identity and timestamps are assigned by the store.
type ActivityStore interface {
	ListActivity(context.Context, string) ([]models.Activity, error)
	AppendActivity(context.Context, string, models.Activity) (*models.Activity, error)
}

var _ ActivityStore = (*ghstore.Client)(nil)

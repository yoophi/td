package ghstore

import "fmt"

// ConflictError reports an observed change; absence of this error does not
// guarantee an atomic write. GitHub does not document conditional issue PATCH.
type ConflictError struct {
	ID         string
	AfterWrite bool
}

func (e *ConflictError) Error() string {
	if e.AfterWrite {
		return fmt.Sprintf("%s changed during update verification; this write may have applied, inspect GitHub before retrying (conflict detection is best-effort)", e.ID)
	}
	return fmt.Sprintf("%s changed since it was read; no write attempted, reload and retry (conflict detection is best-effort)", e.ID)
}

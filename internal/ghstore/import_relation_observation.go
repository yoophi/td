package ghstore

import "context"

// Import may preserve historical links to logically deleted tasks. Only the
// importer sets this private context value; ordinary reads and mutations keep
// the active-task rule. Shared graph and revision fences still validate every
// observed identity and revision through the normal readers.
type importRelationObservationKey struct{}

func withImportRelationObservations(ctx context.Context) context.Context {
	return context.WithValue(ctx, importRelationObservationKey{}, true)
}

func (c *Client) getRelationObservation(ctx context.Context, id string) (*Record, error) {
	if includeDeleted, _ := ctx.Value(importRelationObservationKey{}).(bool); includeDeleted {
		return c.GetIncludingDeleted(ctx, id)
	}
	return c.Get(ctx, id)
}

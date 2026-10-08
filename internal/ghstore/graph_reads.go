package ghstore

import "context"

// List and direct GET can observe different GitHub replicas after creation.
// Do not interpret a missing listed root as deletion when GET confirms it.
// Relationship discovery still uses a paginated, non-atomic listing.
func (c *Client) listWithRoot(ctx context.Context, root string) ([]Record, error) {
	records, err := c.List(ctx, true)
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		if record.ID == root {
			return records, nil
		}
	}
	record, err := c.Get(ctx, root)
	if err != nil {
		return nil, err
	}
	return append(records, *record), nil
}

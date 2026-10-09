package ghstore

import "context"

// ChangeToken fingerprints the complete issue and repository comment
// collections, including deleted/auxiliary entities and old comment edits or
// deletions. Failed or corrupt reads never produce a successful token.
func (c *Client) ChangeToken(ctx context.Context) (string, error) {
	snapshot, err := c.ReadSnapshot(ctx, true)
	if err != nil {
		return "", err
	}
	return snapshot.ChangeToken()
}

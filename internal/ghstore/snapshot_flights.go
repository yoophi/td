package ghstore

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// In-memory fences span credentials for a repository. They protect coalescing
// even when persistent cache access is disabled or fails; no snapshots are kept.
var transientSnapshotReads = struct {
	sync.Mutex
	generations   map[string]uint64
	invalidatedAt map[string]time.Time
	flights       singleflight.Group
}{generations: map[string]uint64{}, invalidatedAt: map[string]time.Time{}}

func (c *Client) snapshotReadGeneration(advance bool) uint64 {
	key := cacheHash("github.com\n" + strings.ToLower(c.repo))
	transientSnapshotReads.Lock()
	defer transientSnapshotReads.Unlock()
	if advance {
		transientSnapshotReads.generations[key]++
		transientSnapshotReads.invalidatedAt[key] = time.Now().UTC()
	}
	return transientSnapshotReads.generations[key]
}
func (c *Client) readSnapshotUncached(ctx context.Context, history bool) (*Snapshot, error) {
	if c.cooldown == nil {
		return c.readSnapshotRemote(ctx, history)
	}
	key := fmt.Sprintf("%s:%d:%t:%t", c.cooldown.key, c.snapshotReadGeneration(false), history, ctx.Value(freshSnapshotKey{}) == true)
	result := transientSnapshotReads.flights.DoChan(key, func() (any, error) { return c.readSnapshotRemote(ctx, history) })
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case observed := <-result:
		if observed.Err != nil {
			return nil, observed.Err
		}
		return observed.Val.(*Snapshot), nil
	}
}

func (c *Client) snapshotReadInvalidatedAt() time.Time {
	key := cacheHash("github.com\n" + strings.ToLower(c.repo))
	transientSnapshotReads.Lock()
	defer transientSnapshotReads.Unlock()
	return transientSnapshotReads.invalidatedAt[key]
}

package ghstore

import (
	"context"
	"fmt"
)

// ExportJournal uses the same two bulk collections as ActionHistory. It never
// refetches individual journals, targets or comments and never bootstraps a
// carrier. Sources remain repository-qualified even when numeric IDs collide.
func (c *Client) ExportJournal(ctx context.Context) (*PortableJournalBundle, error) {
	snapshot, err := c.ReadSnapshot(WithFreshSnapshot(ctx), true)
	if err != nil {
		return nil, err
	}
	actions, err := snapshot.ActionHistory("")
	if err != nil {
		return nil, err
	}
	current, err := newPortableJournal(c.repo, actions)
	if err != nil {
		return nil, err
	}
	archives, err := snapshot.journalArchives(c.repo)
	if err != nil {
		return nil, err
	}
	bundles := []*PortableJournalBundle{current}
	for checksum, archive := range archives {
		if archive.bundle == nil {
			return nil, fmt.Errorf("journal archive %s is incomplete; inspect and finish the original import before exporting complete history", checksum)
		}
		bundles = append(bundles, archive.bundle)
	}
	return mergePortableJournals(bundles...)
}

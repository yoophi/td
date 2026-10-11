package ghstore

import (
	"context"
	"fmt"
)

// CompletedImportMappings observes a complete native plan and accepts only
// unchanged, fully archived imports with verified completion receipts. It does
// not accept public ImportResult counters as proof or allocate any destination.
// The resulting IDs are references, never workflow/review authority.
func (c *Client) CompletedImportMappings(ctx context.Context, items []ImportItem) (map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	plan, err := c.PlanImport(ctx, items, false)
	if err != nil {
		return nil, err
	}
	result := map[string]string{}
	used := map[string]bool{}
	for _, row := range plan.Rows {
		if row.Action != "skip" || row.SourceChanged || !row.HistoryVerified || row.receipt == nil || !canonicalGroupID(row.TargetID) || used[row.TargetID] {
			return nil, fmt.Errorf("import source %s lacks unchanged, verified completion/history for its destination; inspect before workspace entity writes", row.SourceKey)
		}
		used[row.TargetID] = true
		result[row.SourceKey] = row.TargetID
	}
	return result, nil
}

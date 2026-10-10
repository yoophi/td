package ghstore

import (
	"context"
	"fmt"
)

// repairLabels freezes every selected task and complete label payload before
// journal bootstrap, label setup or task writes. Label repair is a scalar
// update; v17 planned groups also permit closed and logically deleted tasks.
func (c *Client) repairLabels(ctx context.Context, records []Record, kind string) (int, error) {
	if len(records) == 0 {
		return 0, nil
	}
	if c.journal != nil && c.journal.Group != nil {
		return 0, fmt.Errorf("%s label repair requires no active group; no write attempted", kind)
	}
	plans := make([]*issueUpdatePlan, 0, len(records))
	for i := range records {
		r := &records[i]
		labels := mirroredPriorityLabels(mirroredLabels(r.Labels, r.Status), r.Priority)
		plan, err := c.prepareIssueUpdate(ctx, r, Changes{Labels: &labels})
		if err != nil {
			return 0, fmt.Errorf("plan %s label repair %s (no write attempted): %w", kind, r.ID, err)
		}
		plans = append(plans, plan)
	}
	grouped := len(plans) > 1 && c.journal != nil
	if grouped {
		if err := c.beginPlannedTaskGroup(ctx, "update", plans, true); err != nil {
			return 0, fmt.Errorf("prepare %s label repair group: %w", kind, err)
		}
	}
	count := 0
	for _, plan := range plans {
		var err error
		if grouped {
			_, _, err = c.ApplyPlannedCommandMember(ctx, plan.observed.ID)
		} else {
			_, err = c.writeIssueUpdate(ctx, plan)
		}
		if err != nil {
			return count, fmt.Errorf("%d %s label repairs saved; %s failed (the failed write may also have applied; inspect current state before retrying): %w", count, kind, plan.observed.ID, err)
		}
		count++
	}
	if grouped {
		groupID := c.journal.Group.Journal.ID
		if _, err := c.CompleteActionGroup(ctx); err != nil {
			return count, fmt.Errorf("%d %s label repairs saved; finish group %s failed (inspect and reconcile before retrying): %w", count, kind, groupID, err)
		}
	}
	return count, nil
}

package ghstore

import (
	"context"
	"fmt"
	"slices"
)

// Model the graph after all unfinished changed rows have shed outgoing links.
// Finished/skipped rows retain their links. Validate final sequencing before
// attempting any detach PATCH; observations will be refreshed again afterwards.
func importDetachedGraphOrder(plan *ImportPlan, relations map[string]importRelations) ([]int, error) {
	clone := *plan
	snapshot := *plan.snapshot
	snapshot.records = slices.Clone(snapshot.records)
	clone.snapshot = &snapshot
	detachedIDs := map[string]bool{}
	for _, row := range plan.Rows {
		if row.Action != "skip" && row.Action != "resume_history" && row.ResumeState != "after_apply" {
			detachedIDs[row.TargetID] = true
		}
	}
	for i := range snapshot.records {
		record := &snapshot.records[i]
		if !detachedIDs[record.ID] {
			continue
		}
		record.ParentID = ""
		if record.Details != nil {
			details := *record.Details
			details.ParentID, details.Dependencies = "", nil
			record.Details = &details
		}
	}
	return importGraphOrder(&clone, relations)
}

// A pre-existing one-phase intent can be upgraded only at its original before
// state. Its final basis/source/archive remain identical. Publish the new
// intent before the detach write so lost responses can be recovered normally.
func (c *Client) prepareImportDetach(ctx context.Context, source ImportItem, row ImportPlanRow, relations importRelations) (ImportPlanRow, error) {
	row.detaching = true
	if row.intent == nil || row.intent.DetachedBasis != "" {
		return row, nil
	}
	digest, err := source.Digest()
	if err != nil || digest != row.Digest {
		return row, fmt.Errorf("source changed since import plan; no detach intent or issue write attempted")
	}
	native, observed, err := c.importNativeTarget(ctx, row.TargetID)
	if err != nil {
		return row, err
	}
	basis, err := importTargetBasis(native)
	if err != nil {
		return row, err
	}
	state, err := row.intent.resumeState(row.Digest, basis)
	if err != nil {
		return row, err
	}
	if state != "before_apply" {
		return row, fmt.Errorf("import detach upgrade requires the original before state")
	}
	_, final, err := importIssueProjection(source, native, observed, relations)
	if err != nil {
		return row, err
	}
	finalBasis, err := importTargetBasis(final)
	if err != nil {
		return row, err
	}
	if finalBasis != row.intent.AfterBasis {
		return row, fmt.Errorf("import projection changed before detach upgrade")
	}
	detached := source
	detached.Issue.ParentID = ""
	_, intermediate, err := importIssueProjection(detached, native, observed, importRelations{})
	if err != nil {
		return row, err
	}
	intermediateBasis, err := importTargetBasis(intermediate)
	if err != nil {
		return row, err
	}
	upgraded := *row.intent
	upgraded.DetachedBasis = intermediateBasis
	if err := c.prepareImport(ctx, upgraded); err != nil {
		return row, err
	}
	row.intent = &upgraded
	return row, nil
}

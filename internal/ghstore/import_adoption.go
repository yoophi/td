package ghstore

import (
	"fmt"
	"reflect"
)

// Adoption is a no-overwrite decision whose retained observation must belong
// to a tracked whole command, including its completion and inverse.
func (c *Client) requireExecutableImportContract(items []ImportItem) error {
	for _, item := range items {
		if item.AdoptTargetID != "" && (c == nil || c.journal == nil || c.journal.Group != nil) {
			return fmt.Errorf("task adoption execution requires an independent tracked command; no API call or write attempted")
		}
	}
	return nil
}

// Source relationships owned by an adopted task are retained as provenance;
// they cannot authorize changing that destination. Reject reverse edges from
// another source that would require mutating an adopted owner.
func validateImportAdoptionRelations(items []ImportItem) error {
	adopted := map[string]bool{}
	for _, item := range items {
		if item.AdoptTargetID != "" {
			adopted[item.SourceKey()] = true
		}
	}
	for _, item := range items {
		if item.AdoptTargetID != "" {
			continue
		}
		for _, edge := range item.Dependencies {
			if edge.RelationType == "blocks" && adopted[item.ReferenceKey(edge.DependsOnID)] {
				return fmt.Errorf("reverse blocks edge requires changing adopted task %s; no write attempted", edge.DependsOnID)
			}
		}
	}
	return nil
}

// A retained destination cannot acquire changes while other members are being
// imported. Compare the complete original native issue/comment observation;
// re-planning must never replace the adoption basis with a newer one.
func verifyImportAdoptionBaseline(original *ImportPlan, current *Snapshot) error {
	for _, row := range original.Rows {
		if row.Action != "adopt" {
			continue
		}
		var before, after *snapshotEntry
		for i := range original.snapshot.entries {
			if fmt.Sprintf("gh-%d", original.snapshot.entries[i].Issue.Number) == row.TargetID {
				before = &original.snapshot.entries[i]
			}
		}
		for i := range current.entries {
			if fmt.Sprintf("gh-%d", current.entries[i].Issue.Number) == row.TargetID {
				after = &current.entries[i]
			}
		}
		if before == nil || after == nil || !reflect.DeepEqual(before, after) {
			return &ConflictError{ID: row.TargetID}
		}
	}
	return nil
}

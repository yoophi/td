package ghstore

import (
	"context"
	"fmt"

	"github.com/marcus/td/internal/models"
)

// Internal batch execution. The CLI remains gated until graph transitions and
// preserved history reads/exports have been validated end to end.
type importBatchResult struct {
	Plan                                                *ImportPlan
	Created, Applied, ArchivedParts, Completed, Skipped int
}

type importDestinationArchive struct {
	Repository string       `json:"repository"`
	Issue      apiIssue     `json:"issue"`
	Comments   []apiComment `json:"comments"`
}

func verifyImportDestinationArchive(row ImportPlanRow, inventory importProvenanceInventory) error {
	if row.intent == nil || row.intent.DestinationArchiveDigest == "" {
		return nil
	}
	ref := importProvenancePart{SourceKey: row.SourceKey, SourceDigest: row.Digest, TargetID: row.TargetID, Kind: "destination", ContentDigest: row.intent.DestinationArchiveDigest}
	return verifyImportDestinationParts(row.TargetID, row.intent.BeforeBasis, inventory[ref.key()])
}

func verifyImportDestinationParts(targetID, expectedBasis string, parts map[int]importProvenancePart) error {
	data, err := reassembleImportProvenance(parts)
	if err != nil {
		return fmt.Errorf("%s prior destination archive: %w", targetID, err)
	}
	var archive importDestinationArchive
	if err := decodeImportJSON(data, &archive); err != nil {
		return err
	}
	basis, err := importTargetBasis(archive.Issue)
	if err != nil {
		return err
	}
	if basis != expectedBasis || fmt.Sprintf("gh-%d", archive.Issue.Number) != targetID {
		return fmt.Errorf("%s prior destination archive binding differs", targetID)
	}
	return nil
}

func addImportParts(inventory importProvenanceInventory, parts []importProvenancePart) {
	for _, part := range parts {
		if inventory[part.key()] == nil {
			inventory[part.key()] = map[int]importProvenancePart{}
		}
		inventory[part.key()][part.Index] = part
	}
}

func (c *Client) executeImport(ctx context.Context, items []ImportItem, force bool) (*importBatchResult, error) {
	result := &importBatchResult{}
	fail := func(stage, id string, err error) (*importBatchResult, error) {
		return result, fmt.Errorf("import %s %s failed (created=%d, applied=%d, archived_parts=%d, completed=%d); writes may already have occurred; refresh remote state before resuming, no automatic retry: %w", stage, id, result.Created, result.Applied, result.ArchivedParts, result.Completed, err)
	}
	plan, err := c.PlanImport(ctx, items, force)
	if err != nil {
		return fail("plan", "", err)
	}
	result.Plan = plan
	if len(items) == 0 {
		plan.DryRun = false
		return result, nil
	}
	initial := plan
	for i, row := range plan.Rows {
		if row.Action != "create" {
			continue
		}
		source := items[i].Issue
		skeleton := models.Issue{Title: source.Title, Description: source.Description, Type: source.Type, Priority: source.Priority, Points: source.Points, Acceptance: source.Acceptance, Labels: source.Labels, Status: models.StatusOpen}
		record, err := c.createWithOperationID(ctx, &skeleton, row.OperationID)
		if err != nil {
			return fail("allocate", row.SourceKey, err)
		}
		result.Created++
		plan.Rows[i].TargetID = record.ID
	}
	// One full re-observation recovers all allocated IDs and durable intents.
	// Keep the decision basis for destinations that existed before allocation.
	if result.Created > 0 {
		plan, err = c.PlanImport(ctx, items, force)
		if err != nil {
			return fail("replan", "", err)
		}
	}
	result.Plan = plan
	mapping := map[string]string{}
	for i, row := range plan.Rows {
		prior := initial.Rows[i]
		if prior.basis != "" && prior.basis != row.basis {
			return fail("replan", row.TargetID, &ConflictError{ID: row.TargetID})
		}
		mapping[row.SourceKey] = row.TargetID
	}
	relations, err := importRelationshipProjection(items, mapping)
	if err != nil {
		return fail("relationships", "", err)
	}
	order, err := importGraphOrder(plan, relations)
	if err != nil {
		return fail("graph order", "", err)
	}
	inventory, err := snapshotImportProvenance(plan.snapshot)
	if err != nil {
		return fail("history inventory", "", err)
	}
	// Preserve the source and any overwritten destination before field PATCHes.
	for i := range plan.Rows {
		row := &plan.Rows[i]
		if row.Action == "skip" {
			result.Skipped++
			continue
		}
		if err := verifyImportDestinationArchive(*row, inventory); err != nil {
			return fail("verify prior history", row.TargetID, err)
		}
		if row.Action == "overwrite" && row.intent == nil {
			var entry *snapshotEntry
			for j := range plan.snapshot.entries {
				if fmt.Sprintf("gh-%d", plan.snapshot.entries[j].Issue.Number) == row.TargetID {
					entry = &plan.snapshot.entries[j]
					break
				}
			}
			if entry == nil {
				return fail("archive destination", row.TargetID, fmt.Errorf("destination missing from checked snapshot"))
			}
			var parts []importProvenancePart
			if row.capture != nil {
				parts, err = recoverImportDestinationCapture(*row.capture, c.repo, *row, *entry)
			} else {
				var capture importCapture
				capture, parts, err = newImportCapture(c.repo, *row, *entry)
				if err == nil {
					var body string
					body, err = renderImportCapture(capture)
					if err == nil {
						err = c.publishImportCheckpoint(ctx, row.TargetID, row.basis, body, "destination capture")
					}
				}
			}
			if err != nil {
				return fail("archive destination", row.TargetID, err)
			}
			saved, err := c.writeImportProvenance(ctx, parts, inventory, row.basis)
			result.ArchivedParts += saved
			if err != nil {
				return fail("archive destination", row.TargetID, err)
			}
			addImportParts(inventory, parts)
			row.destinationArchiveDigest = parts[0].ContentDigest
			row.destinationArchiveBasis = row.basis
		}
		parts, err := sourceImportProvenance(items[i], row.TargetID)
		if err != nil {
			return fail("archive source", row.TargetID, err)
		}
		saved, err := c.writeImportProvenance(ctx, parts, inventory, row.basis)
		result.ArchivedParts += saved
		if err != nil {
			return fail("archive source", row.TargetID, err)
		}
		addImportParts(inventory, parts)
	}
	// Graph checks remain the shared checked mutation path, including conflict
	// fences around dependencies and parent chains.
	receipts := make([]importReceipt, 0, len(order))
	for _, i := range order {
		row := plan.Rows[i]
		basis := row.basis
		if row.Action != "resume_history" {
			_, after, patched, err := c.applyImportIssue(ctx, items[i], row, relations[row.SourceKey])
			if patched {
				result.Applied++
			}
			if err != nil {
				return fail("apply", row.TargetID, err)
			}
			basis = after
		}
		parts, err := sourceImportProvenance(items[i], row.TargetID)
		if err != nil {
			return fail("complete", row.TargetID, err)
		}
		receipt := importReceipt{Version: 1, SourceKey: row.SourceKey, SourceDigest: row.Digest, TargetID: row.TargetID, TargetBasis: basis, SourceParts: len(parts)}
		if row.destinationArchiveDigest != "" {
			receipt.DestinationArchiveDigest, receipt.DestinationBasis = row.destinationArchiveDigest, row.destinationArchiveBasis
		} else if row.intent != nil && row.intent.DestinationArchiveDigest != "" {
			receipt.DestinationArchiveDigest, receipt.DestinationBasis = row.intent.DestinationArchiveDigest, row.intent.BeforeBasis
		}
		receipts = append(receipts, receipt)
	}
	if len(receipts) > 0 {
		snapshot, err := c.ReadSnapshot(WithFreshSnapshot(ctx), true)
		if err != nil {
			return fail("completion inventory", "", err)
		}
		inventory, err = snapshotImportProvenance(snapshot)
		if err != nil {
			return fail("completion inventory", "", err)
		}
	}
	for _, receipt := range receipts {
		if err := c.completeImportWithInventory(ctx, receipt, inventory); err != nil {
			return fail("complete", receipt.TargetID, err)
		}
		result.Completed++
	}
	plan.DryRun = false
	return result, nil
}

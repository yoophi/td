package ghstore

import (
	"encoding/json"
	"fmt"

	"github.com/marcus/td/internal/models"
)

// Annotate the first native comment of each archive set using only the already
// observed comments. Raw messages remain unchanged for lossless export/replay;
// Kind/Native/session/operation fields are never promoted to active events.
func annotateImportHistory(snapshot *Snapshot) error {
	inventory, err := snapshotImportProvenance(snapshot)
	if err != nil {
		return err
	}
	receipts, _, err := snapshotImportReceipts(snapshot)
	if err != nil {
		return err
	}
	for _, receipt := range receipts {
		if receipt.SourceParts > 0 {
			if _, err := verifyImportReceiptHistory(receipt, inventory); err != nil {
				return err
			}
		}
	}
	documents := map[string]*models.PreservedHistory{}
	anchorIndexes := map[string]int{}
	for key, parts := range inventory {
		var first importProvenancePart
		minimum := int(^uint(0) >> 1)
		for _, part := range parts {
			first = part
			if part.Index < minimum {
				minimum = part.Index
			}
		}
		anchorIndexes[key] = minimum
		doc := &models.PreservedHistory{Kind: first.Kind, SourceKey: first.SourceKey, SourceDigest: first.SourceDigest, ContentDigest: first.ContentDigest, PartsPresent: len(parts), PartsExpected: first.Count, Complete: len(parts) == first.Count}
		if doc.Complete {
			data, err := reassembleImportProvenance(parts)
			if err != nil {
				return err
			}
			if first.Kind == "source" {
				if _, err := recoverImportSource(parts); err != nil {
					return err
				}
			} else {
				var archive importDestinationArchive
				if err := decodeImportJSON(data, &archive); err != nil {
					return err
				}
				if fmt.Sprintf("gh-%d", archive.Issue.Number) != first.TargetID {
					return fmt.Errorf("preserved destination history refers to another issue")
				}
			}
			doc.Data = json.RawMessage(data)
		}
		documents[key] = doc
	}
	for _, entry := range snapshot.entries {
		id := fmt.Sprintf("gh-%d", entry.Issue.Number)
		anchors := map[string]*models.PreservedHistory{}
		for _, comment := range entry.Comments {
			part, err := decodeImportProvenance(comment.Body)
			if err != nil {
				return err
			}
			if part != nil && part.Index == anchorIndexes[part.key()] {
				anchors[fmt.Sprintf("ghc-%d", comment.ID)] = documents[part.key()]
			}
		}
		for index := range snapshot.activities[id] {
			activity := &snapshot.activities[id][index]
			if doc := anchors[activity.ID]; doc != nil {
				activity.PreservedHistory = doc
			}
		}
	}
	return nil
}

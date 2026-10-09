package ghstore

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

const importReceiptPrefix = "<!-- td:import-receipt:"
const importReceiptStart = importReceiptPrefix + "v1\n"

// A receipt is a completion record, never an active workflow approval. It is
// stored as a native comment; normal activity parsing does not grant it rights.
type importReceipt struct {
	Version                  int    `json:"version"`
	SourceKey                string `json:"source_key"`
	SourceDigest             string `json:"source_digest"`
	TargetID                 string `json:"target_id"`
	TargetBasis              string `json:"target_basis"`
	SourceParts              int    `json:"source_parts,omitempty"`
	DestinationArchiveDigest string `json:"destination_archive_digest,omitempty"`
	DestinationBasis         string `json:"destination_basis,omitempty"`
	commentID                int64
}

var importDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (receipt importReceipt) validate() error {
	parts := strings.Split(receipt.SourceKey, ":")
	validSource := len(parts) == 2 && parts[0] == "legacy" && importIDPattern.MatchString(parts[1])
	if len(parts) == 3 && importIDPattern.MatchString(parts[2]) {
		validSource = (parts[0] == "legacy" && importNamespacePattern.MatchString(parts[1])) || (parts[0] == "github" && repositoryPattern.MatchString(parts[1]))
	}
	n, err := Number(receipt.TargetID)
	if receipt.Version != 1 || !validSource || err != nil || receipt.SourceParts < 0 || receipt.TargetID != fmt.Sprintf("gh-%d", n) || !importDigestPattern.MatchString(receipt.SourceDigest) || !importDigestPattern.MatchString(receipt.TargetBasis) {
		return fmt.Errorf("invalid or unsupported import completion receipt")
	}
	if (receipt.DestinationArchiveDigest == "") != (receipt.DestinationBasis == "") || (receipt.DestinationArchiveDigest != "" && (!importDigestPattern.MatchString(receipt.DestinationArchiveDigest) || !importDigestPattern.MatchString(receipt.DestinationBasis))) {
		return fmt.Errorf("invalid import prior destination proof")
	}
	return nil
}

func verifyImportReceiptHistory(receipt importReceipt, inventory importProvenanceInventory) (bool, error) {
	if receipt.SourceParts == 0 {
		// Prototype receipts prove fields only, never that history was archived.
		return false, nil
	}
	ref := importProvenancePart{SourceKey: receipt.SourceKey, SourceDigest: receipt.SourceDigest, TargetID: receipt.TargetID, Kind: "source", ContentDigest: receipt.SourceDigest}
	parts := inventory[ref.key()]
	if len(parts) != receipt.SourceParts {
		return false, fmt.Errorf("%s completion history is incomplete: expected %d parts, found %d; no import writes attempted", receipt.TargetID, receipt.SourceParts, len(parts))
	}
	source, err := recoverImportSource(parts)
	if err != nil {
		return false, fmt.Errorf("%s completion history verification failed: %w", receipt.TargetID, err)
	}
	if parts[0].Count != receipt.SourceParts || source.SourceKey() != receipt.SourceKey {
		return false, fmt.Errorf("%s completion history binding differs", receipt.TargetID)
	}
	if receipt.DestinationArchiveDigest != "" {
		ref.Kind, ref.ContentDigest = "destination", receipt.DestinationArchiveDigest
		if err := verifyImportDestinationParts(receipt.TargetID, receipt.DestinationBasis, inventory[ref.key()]); err != nil {
			return false, err
		}
	}
	return true, nil
}

func renderImportReceipt(receipt importReceipt) (string, error) {
	if err := receipt.validate(); err != nil {
		return "", err
	}
	data, err := json.Marshal(receipt)
	if err != nil {
		return "", err
	}
	return "td import completed.\n\n" + importReceiptStart + string(data) + markerEnd, nil
}

func decodeImportReceipt(body string) (*importReceipt, error) {
	index := strings.Index(body, importReceiptPrefix)
	if index < 0 {
		return nil, nil
	}
	block := strings.TrimSpace(body[index:])
	if strings.Count(body, importReceiptPrefix) != 1 || !strings.HasPrefix(block, importReceiptStart) || !strings.HasSuffix(block, markerEnd) {
		return nil, fmt.Errorf("malformed or unsupported import receipt; inspect the edited comment")
	}
	var receipt importReceipt
	data := strings.TrimSuffix(strings.TrimPrefix(block, importReceiptStart), markerEnd)
	if err := decodeImportJSON([]byte(data), &receipt); err != nil {
		return nil, fmt.Errorf("invalid import receipt: %w", err)
	}
	expected, err := renderImportReceipt(receipt)
	if err != nil {
		return nil, err
	}
	if body != expected {
		return nil, fmt.Errorf("import receipt text and metadata disagree; inspect the edited comment")
	}
	return &receipt, nil
}

// Native clocks change when comments are added. Compare writable issue fields
// rather than timestamps, while retaining native state/body/labels and identity.
func importTargetBasis(item apiIssue) (string, error) {
	item.CreatedAt, item.UpdatedAt, item.ClosedAt = time.Time{}, time.Time{}, nil
	item.Labels = slices.Clone(item.Labels)
	if item.Labels == nil {
		item.Labels = []struct {
			Name string `json:"name"`
		}{}
	}
	slices.SortFunc(item.Labels, func(a, b struct {
		Name string `json:"name"`
	}) int {
		return strings.Compare(a.Name, b.Name)
	})
	data, err := json.Marshal(item)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

func snapshotImportReceipts(snapshot *Snapshot) (map[string]importReceipt, map[string]string, error) {
	receipts := map[string]importReceipt{}
	bases := map[string]string{}
	for _, entry := range snapshot.entries {
		id := fmt.Sprintf("gh-%d", entry.Issue.Number)
		basis, err := importTargetBasis(entry.Issue)
		if err != nil {
			return nil, nil, err
		}
		bases[id] = basis
		for _, comment := range entry.Comments {
			receipt, err := decodeImportReceipt(comment.Body)
			if err != nil {
				return nil, nil, fmt.Errorf("import receipt comment %d on %s: %w", comment.ID, id, err)
			}
			if receipt == nil {
				continue
			}
			if receipt.TargetID != id {
				return nil, nil, fmt.Errorf("import receipt comment %d refers to another issue", comment.ID)
			}
			receipt.commentID = comment.ID
			if prior, ok := receipts[receipt.SourceKey]; ok && prior.TargetID != id {
				return nil, nil, fmt.Errorf("import source %s has completion receipts on multiple targets", receipt.SourceKey)
			}
			// Entries' comments are sorted by native ID, so the last completion
			// for the same source/target is the latest completed import version.
			receipts[receipt.SourceKey] = *receipt
		}
	}
	return receipts, bases, nil
}

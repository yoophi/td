package ghstore

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

const importProvenancePrefix = "<!-- td:import-provenance:"
const importProvenanceStart = importProvenancePrefix + "v1\n"
const importProvenanceChunkBytes = 32 * 1024

// Encoded source data stays a native comment, never td activity metadata.
// Chunking preserves large histories without exceeding one comment's limit.
type importProvenancePart struct {
	Version       int    `json:"version"`
	SourceKey     string `json:"source_key"`
	SourceDigest  string `json:"source_digest"`
	TargetID      string `json:"target_id"`
	Kind          string `json:"kind"`
	ContentDigest string `json:"content_digest"`
	Index         int    `json:"index"`
	Count         int    `json:"count"`
	Data          string `json:"data"`
}

func (part importProvenancePart) key() string {
	return strings.Join([]string{part.SourceKey, part.SourceDigest, part.TargetID, part.Kind, part.ContentDigest}, "\n")
}

func (part importProvenancePart) validate() error {
	ref := importReceipt{Version: part.Version, SourceKey: part.SourceKey, SourceDigest: part.SourceDigest, TargetID: part.TargetID, TargetBasis: part.ContentDigest}
	if err := ref.validate(); err != nil {
		return fmt.Errorf("invalid import provenance: %w", err)
	}
	if (part.Kind != "source" && part.Kind != "destination") || part.Index < 0 || part.Count < 1 || part.Index >= part.Count || len(part.Data) > base64.StdEncoding.EncodedLen(importProvenanceChunkBytes) {
		return fmt.Errorf("invalid import provenance part fields")
	}
	data, err := base64.StdEncoding.Strict().DecodeString(part.Data)
	if err != nil || len(data) == 0 || len(data) > importProvenanceChunkBytes || base64.StdEncoding.EncodeToString(data) != part.Data {
		return fmt.Errorf("invalid import provenance part data")
	}
	if part.Kind == "source" && part.ContentDigest != part.SourceDigest {
		return fmt.Errorf("source provenance digest disagrees with import source digest")
	}
	return nil
}

func renderImportProvenance(part importProvenancePart) (string, error) {
	if err := part.validate(); err != nil {
		return "", err
	}
	data, err := json.Marshal(part)
	if err != nil {
		return "", err
	}
	body := fmt.Sprintf("Imported history (%s, part %d/%d).\n\n%s%s%s", part.Kind, part.Index+1, part.Count, importProvenanceStart, data, markerEnd)
	if len(body) > 60000 {
		return "", fmt.Errorf("import provenance comment exceeds the conservative 60000-byte publication limit")
	}
	return body, nil
}

func decodeImportProvenance(body string) (*importProvenancePart, error) {
	index := strings.Index(body, importProvenancePrefix)
	if index < 0 {
		return nil, nil
	}
	block := strings.TrimSpace(body[index:])
	if strings.Count(body, importProvenancePrefix) != 1 || !strings.HasPrefix(block, importProvenanceStart) || !strings.HasSuffix(block, markerEnd) {
		return nil, fmt.Errorf("malformed or unsupported import provenance; inspect the edited comment")
	}
	var part importProvenancePart
	data := strings.TrimSuffix(strings.TrimPrefix(block, importProvenanceStart), markerEnd)
	if err := decodeImportJSON([]byte(data), &part); err != nil {
		return nil, fmt.Errorf("invalid import provenance: %w", err)
	}
	expected, err := renderImportProvenance(part)
	if err != nil {
		return nil, err
	}
	if body != expected {
		return nil, fmt.Errorf("import provenance text and metadata disagree; inspect the edited comment")
	}
	return &part, nil
}

func importProvenanceParts(sourceKey, sourceDigest, targetID, kind string, data []byte) ([]importProvenancePart, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("import provenance content is empty")
	}
	count := (len(data)-1)/importProvenanceChunkBytes + 1
	contentDigest := fmt.Sprintf("%x", sha256.Sum256(data))
	result := make([]importProvenancePart, 0, count)
	for index, offset := 0, 0; offset < len(data); index, offset = index+1, offset+importProvenanceChunkBytes {
		end := min(offset+importProvenanceChunkBytes, len(data))
		part := importProvenancePart{Version: 1, SourceKey: sourceKey, SourceDigest: sourceDigest, TargetID: targetID, Kind: kind, ContentDigest: contentDigest, Index: index, Count: count, Data: base64.StdEncoding.EncodeToString(data[offset:end])}
		if err := part.validate(); err != nil {
			return nil, err
		}
		result = append(result, part)
	}
	return result, nil
}

func sourceImportProvenance(source ImportItem, targetID string) ([]importProvenancePart, error) {
	data, err := json.Marshal(source)
	if err != nil {
		return nil, err
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(data))
	return importProvenanceParts(source.SourceKey(), digest, targetID, "source", data)
}

func recoverImportSource(parts map[int]importProvenancePart) (*ImportItem, error) {
	data, err := reassembleImportProvenance(parts)
	if err != nil {
		return nil, err
	}
	first := parts[0]
	if first.Kind != "source" {
		return nil, fmt.Errorf("destination archive is not an import source envelope")
	}
	var source ImportItem
	if err := source.decodeJSON(data, true); err != nil {
		return nil, fmt.Errorf("invalid recovered import source: %w", err)
	}
	if source.SourceKey() != first.SourceKey {
		return nil, fmt.Errorf("recovered import source identity differs from its provenance")
	}
	return &source, nil
}

type importProvenanceInventory map[string]map[int]importProvenancePart

func snapshotImportProvenance(snapshot *Snapshot) (importProvenanceInventory, error) {
	inventory := importProvenanceInventory{}
	for _, entry := range snapshot.entries {
		id := fmt.Sprintf("gh-%d", entry.Issue.Number)
		for _, comment := range entry.Comments {
			part, err := decodeImportProvenance(comment.Body)
			if err != nil {
				return nil, fmt.Errorf("import provenance comment %d on %s: %w", comment.ID, id, err)
			}
			if part == nil {
				continue
			}
			if part.TargetID != id {
				return nil, fmt.Errorf("import provenance comment %d refers to another target", comment.ID)
			}
			key := part.key()
			if inventory[key] == nil {
				inventory[key] = map[int]importProvenancePart{}
			}
			if prior, ok := inventory[key][part.Index]; ok {
				return nil, fmt.Errorf("duplicate import provenance part %d on %s (expected count %d); inspect comments before continuing", part.Index+1, id, prior.Count)
			}
			for _, prior := range inventory[key] {
				if prior.Count != part.Count {
					return nil, fmt.Errorf("inconsistent import provenance part counts on %s", id)
				}
				break
			}
			inventory[key][part.Index] = *part
		}
	}
	return inventory, nil
}

func reassembleImportProvenance(parts map[int]importProvenancePart) ([]byte, error) {
	first, ok := parts[0]
	if !ok || len(parts) != first.Count {
		return nil, fmt.Errorf("import provenance is incomplete")
	}
	var data []byte
	for index := 0; index < first.Count; index++ {
		part, ok := parts[index]
		if !ok || part.key() != first.key() || part.Index != index || part.Count != first.Count {
			return nil, fmt.Errorf("import provenance parts are inconsistent")
		}
		if err := part.validate(); err != nil {
			return nil, err
		}
		chunk, err := base64.StdEncoding.Strict().DecodeString(part.Data)
		if err != nil {
			return nil, err
		}
		data = append(data, chunk...)
	}
	if fmt.Sprintf("%x", sha256.Sum256(data)) != first.ContentDigest {
		return nil, fmt.Errorf("import provenance content digest mismatch; inspect edited comments")
	}
	return data, nil
}

// Inventory comes from one bulk comment observation. Only missing parts are
// posted; native issue content is checked once before and after the whole set.
func (c *Client) writeImportProvenance(ctx context.Context, expected []importProvenancePart, inventory importProvenanceInventory, basis string) (int, error) {
	if len(expected) == 0 {
		return 0, fmt.Errorf("import provenance requires parts")
	}
	first := expected[0]
	known := inventory[first.key()]
	for index, part := range known {
		if index < 0 || index >= len(expected) || part.Count != len(expected) {
			return 0, fmt.Errorf("existing provenance part count disagrees; no comments attempted")
		}
	}
	missing := []string{}
	for index, part := range expected {
		if part.key() != first.key() || part.Index != index || part.Count != len(expected) {
			return 0, fmt.Errorf("expected import provenance parts are inconsistent")
		}
		body, err := renderImportProvenance(part)
		if err != nil {
			return 0, err
		}
		if prior, ok := known[index]; ok {
			if prior != part {
				return 0, fmt.Errorf("existing import provenance part %d differs; no comments attempted", index+1)
			}
			continue
		}
		missing = append(missing, body)
	}
	// Verify the expected complete payload before posting the first part.
	all := map[int]importProvenancePart{}
	for _, part := range expected {
		all[part.Index] = part
	}
	if _, err := reassembleImportProvenance(all); err != nil {
		return 0, err
	}
	record, actual, err := c.importTarget(ctx, first.TargetID)
	if err != nil {
		return 0, err
	}
	if actual != basis {
		return 0, &ConflictError{ID: first.TargetID}
	}
	saved := 0
	if err := c.fenceImportExecution(ctx); err != nil {
		return 0, err
	}
	for _, body := range missing {
		data, err := c.request(ctx, "POST", fmt.Sprintf("/issues/%d/comments", record.Number), map[string]string{"body": body}, false)
		if err != nil {
			return saved, fmt.Errorf("%d provenance parts saved; failed part may have applied; re-read comments before retrying: %w", saved, err)
		}
		var comment apiComment
		if err := json.Unmarshal(data, &comment); err != nil || comment.ID <= 0 || comment.Body != body {
			return saved, fmt.Errorf("%d provenance parts saved; current response unreadable or different; inspect comments before retrying", saved)
		}
		saved++
	}
	_, actual, err = c.importTarget(withReadbackCost(ctx), first.TargetID)
	if err != nil {
		return saved, err
	}
	if actual != basis {
		return saved, &ConflictError{ID: first.TargetID, AfterWrite: saved > 0}
	}
	if err := c.fenceImportExecution(ctx); err != nil {
		return saved, err
	}
	return saved, nil
}

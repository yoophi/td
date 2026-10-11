package ghstore

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const importIntentPrefix = "<!-- td:import-intent:"

// An intent precedes issue PATCHes. Known before/detached/final bases can be
// resumed; any other state means a subsequent edit and must not be replaced.
type importIntent struct {
	Version                  int                   `json:"version"`
	Owner                    *ImportExecutionOwner `json:"owner,omitempty"`
	SourceKey                string                `json:"source_key"`
	SourceDigest             string                `json:"source_digest"`
	TargetID                 string                `json:"target_id"`
	BeforeBasis              string                `json:"before_basis"`
	AfterBasis               string                `json:"after_basis"`
	DetachedBasis            string                `json:"detached_basis,omitempty"`
	DestinationArchiveDigest string                `json:"destination_archive_digest,omitempty"`
	commentID                int64
}

func (intent importIntent) validate() error {
	receipt := importReceipt{Version: intent.Version, Owner: intent.Owner, SourceKey: intent.SourceKey, SourceDigest: intent.SourceDigest, TargetID: intent.TargetID, TargetBasis: intent.BeforeBasis}
	if err := receipt.validate(); err != nil {
		return fmt.Errorf("invalid import intent: %w", err)
	}
	if !importDigestPattern.MatchString(intent.AfterBasis) || (intent.DestinationArchiveDigest != "" && !importDigestPattern.MatchString(intent.DestinationArchiveDigest)) {
		return fmt.Errorf("invalid import intent expected content basis")
	}
	if intent.DetachedBasis != "" && !importDigestPattern.MatchString(intent.DetachedBasis) {
		return fmt.Errorf("invalid detached import content basis")
	}
	return nil
}

func renderImportIntent(intent importIntent) (string, error) {
	if err := intent.validate(); err != nil {
		return "", err
	}
	data, err := json.Marshal(intent)
	if err != nil {
		return "", err
	}
	return "td import prepared.\n\n" + importCheckpointStart(importIntentPrefix, intent.Version) + string(data) + markerEnd, nil
}

func decodeImportIntent(body string) (*importIntent, error) {
	index := strings.Index(body, importIntentPrefix)
	if index < 0 {
		return nil, nil
	}
	data, err := importCheckpointJSON(body, importIntentPrefix)
	if err != nil {
		return nil, err
	}
	var intent importIntent
	if err := decodeImportJSON(data, &intent); err != nil {
		return nil, fmt.Errorf("invalid import intent: %w", err)
	}
	expected, err := renderImportIntent(intent)
	if err != nil {
		return nil, err
	}
	if body != expected {
		return nil, fmt.Errorf("import intent text and metadata disagree; inspect the edited comment")
	}
	return &intent, nil
}

func (intent importIntent) resumeState(sourceDigest, basis string) (string, error) {
	if sourceDigest != intent.SourceDigest {
		return "", fmt.Errorf("source %s changed during unfinished import; resume with the original input before starting a different import", intent.SourceKey)
	}
	// Prefer after_apply for a no-op PATCH where both bases are identical.
	if basis == intent.AfterBasis {
		return "after_apply", nil
	}
	if intent.DetachedBasis != "" && basis == intent.DetachedBasis {
		return "after_detach", nil
	}
	if basis == intent.BeforeBasis {
		return "before_apply", nil
	}
	return "", fmt.Errorf("destination changed during unfinished import: %w", &ConflictError{ID: intent.TargetID})
}

func snapshotImportIntents(snapshot *Snapshot) (map[string]importIntent, error) {
	intents := map[string]importIntent{}
	for _, entry := range snapshot.entries {
		id := fmt.Sprintf("gh-%d", entry.Issue.Number)
		for _, comment := range entry.Comments {
			intent, err := decodeImportIntent(comment.Body)
			if err != nil {
				return nil, fmt.Errorf("import intent comment %d on %s: %w", comment.ID, id, err)
			}
			if intent == nil {
				continue
			}
			if intent.TargetID != id {
				return nil, fmt.Errorf("import intent comment %d refers to another issue", comment.ID)
			}
			if prior, ok := intents[intent.SourceKey]; ok && prior.TargetID != id {
				return nil, fmt.Errorf("import source %s has prepared intents on multiple targets", intent.SourceKey)
			}
			intent.commentID = comment.ID
			intents[intent.SourceKey] = *intent
		}
	}
	return intents, nil
}

func (c *Client) importTarget(ctx context.Context, id string) (*Record, string, error) {
	item, record, err := c.importNativeTarget(ctx, id)
	if err != nil {
		return nil, "", err
	}
	basis, err := importTargetBasis(item)
	return record, basis, err
}

func (c *Client) importNativeTarget(ctx context.Context, id string) (apiIssue, *Record, error) {
	n, err := Number(id)
	if err != nil {
		return apiIssue{}, nil, err
	}
	data, err := c.request(ctx, "GET", fmt.Sprintf("/issues/%d", n), nil, false)
	if err != nil {
		return apiIssue{}, nil, err
	}
	var item apiIssue
	if err := json.Unmarshal(data, &item); err != nil {
		return apiIssue{}, nil, err
	}
	record, err := item.record()
	if err != nil {
		return apiIssue{}, nil, err
	}
	if record.Number != n || (record.meta.EntityKind != "" && record.meta.EntityKind != "issue") {
		return apiIssue{}, nil, fmt.Errorf("import target %s is not the expected task issue", id)
	}
	record.repository = c.repo
	return item, record, nil
}

// prepareImport publishes one checked intent, never retrying an uncertain
// comment POST. The caller must replan before resuming or attempting a PATCH.
func (c *Client) prepareImport(ctx context.Context, intent importIntent) error {
	var err error
	intent.Version, intent.Owner, err = importCheckpointOwnership(ctx, intent.Owner)
	if err != nil {
		return err
	}
	body, err := renderImportIntent(intent)
	if err != nil {
		return err
	}
	return c.publishImportCheckpoint(ctx, intent.TargetID, intent.BeforeBasis, body, "intent")
}

func (c *Client) completeImport(ctx context.Context, receipt importReceipt) error {
	_, err := renderImportReceipt(receipt)
	if err != nil {
		return err
	}
	if receipt.SourceParts == 0 {
		return fmt.Errorf("completion requires verified source provenance parts; no receipt POST attempted")
	}
	snapshot, err := c.ReadSnapshot(WithFreshSnapshot(ctx), true)
	if err != nil {
		return err
	}
	inventory, err := snapshotImportProvenance(snapshot)
	if err != nil {
		return err
	}
	return c.completeImportWithInventory(ctx, receipt, inventory)
}

// Batch completion shares one fresh bulk inventory after all field writes.
// Each publication still checks its own destination before/after its POST.
func (c *Client) completeImportWithInventory(ctx context.Context, receipt importReceipt, inventory importProvenanceInventory) error {
	var err error
	receipt.Version, receipt.Owner, err = importCheckpointOwnership(ctx, receipt.Owner)
	if err != nil {
		return err
	}
	if receipt.SourceParts == 0 {
		return fmt.Errorf("completion requires verified source provenance parts; no receipt POST attempted")
	}
	body, err := renderImportReceipt(receipt)
	if err != nil {
		return err
	}
	if _, err := verifyImportReceiptHistory(receipt, inventory); err != nil {
		return err
	}
	return c.publishImportCheckpoint(ctx, receipt.TargetID, receipt.TargetBasis, body, "completion receipt")
}

func (c *Client) publishImportCheckpoint(ctx context.Context, targetID, expectedBasis, body, kind string) error {
	record, basis, err := c.importTarget(ctx, targetID)
	if err != nil {
		return err
	}
	if basis != expectedBasis {
		return &ConflictError{ID: targetID}
	}
	if err := c.fenceImportExecution(ctx); err != nil {
		return err
	}
	data, err := c.request(ctx, "POST", fmt.Sprintf("/issues/%d/comments", record.Number), map[string]string{"body": body}, false)
	if err != nil {
		return fmt.Errorf("%w; import %s may exist on %s; inspect or replan before retrying; issue PATCH not attempted by checkpoint publication", err, kind, targetID)
	}
	var comment apiComment
	if err := json.Unmarshal(data, &comment); err != nil || comment.ID <= 0 || comment.Body != body {
		return fmt.Errorf("import %s response unreadable or different on %s; inspect or replan before retrying; issue PATCH not attempted by checkpoint publication", kind, targetID)
	}
	if scope := importExecution(ctx); scope != nil {
		owner, err := c.commentIssueNumber(comment.IssueURL)
		if err != nil || owner != record.Number || !validJournalPartNative(comment) || !strings.EqualFold(comment.User.Login, scope.root.Author) || comment.CreatedAt.Before(scope.root.CreatedAt) {
			return fmt.Errorf("owned import checkpoint has unverified native author, target or clocks")
		}
	}
	_, basis, err = c.importTarget(ctx, targetID)
	if err != nil {
		return fmt.Errorf("%s saved on %s but readback failed; issue PATCH not attempted by checkpoint publication: %w", kind, targetID, err)
	}
	if basis != expectedBasis {
		return fmt.Errorf("%s saved but destination changed; issue PATCH not attempted by checkpoint publication: %w", kind, &ConflictError{ID: targetID})
	}
	return nil
}

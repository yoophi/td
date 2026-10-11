package ghstore

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
)

type ImportPlanRow struct {
	SourceID                 string   `json:"source_id"`
	SourceKey                string   `json:"source_key"`
	Digest                   string   `json:"digest"`
	TargetID                 string   `json:"target_id,omitempty"`
	Action                   string   `json:"action"`
	SourceChanged            bool     `json:"source_changed,omitempty"`
	ParentSourceKey          string   `json:"parent_source_key,omitempty"`
	DependencySourceKeys     []string `json:"dependency_source_keys"`
	BlockedSourceKeys        []string `json:"blocked_source_keys"`
	Observed                 *Record  `json:"-"`
	OperationID              string   `json:"operation_id"`
	ResumeState              string   `json:"resume_state,omitempty"`
	HistoryVerified          bool     `json:"history_verified,omitempty"`
	intent                   *importIntent
	basis                    string
	destinationArchiveDigest string
	destinationArchiveBasis  string
	capture                  *importCapture
	detaching                bool
}

// The creation operation is stored atomically with the issue body. Using the
// source identity, rather than its contents, lets a later invocation recover
// an uncertain creation even when the input has changed. Resume is not proof
// that relationships or history were fully imported.
func importOperationID(sourceKey string) string {
	return fmt.Sprintf("td-import-v1-%x", sha256.Sum256([]byte(sourceKey)))
}

type ImportPlan struct {
	Version    int             `json:"version"`
	Repository string          `json:"repository"`
	DryRun     bool            `json:"dry_run"`
	Rows       []ImportPlanRow `json:"rows"`
	Warnings   []string        `json:"warnings"`
	snapshot   *Snapshot
}

// PlanImport validates every source before observing targets. New GitHub
// numbers cannot be allocated by dry-run; relationships use stable source keys.
func (c *Client) PlanImport(ctx context.Context, items []ImportItem, force bool) (*ImportPlan, error) {
	frozen, err := freezeImportItems(items)
	if err != nil {
		return nil, err
	}
	return c.planImport(ctx, frozen, force)
}

// Internal replans share the command's frozen input; only observed remote
// results and allocated destination IDs may change across plans.
func (c *Client) planImport(ctx context.Context, items []ImportItem, force bool) (*ImportPlan, error) {
	if err := ValidateImportItems(items); err != nil {
		return nil, err
	}
	plan := &ImportPlan{Version: 1, Repository: c.repo, DryRun: true, Rows: []ImportPlanRow{}, Warnings: []string{
		"Native authors and timestamps cannot be impersonated; originals will be retained as provenance.",
		"Historical reviews are not active approvals in the destination repository.",
		"GitHub writes and conflict checks are best-effort, not an atomic transaction.",
	}}
	if len(items) == 0 {
		return plan, nil
	}
	snapshot, err := c.ReadSnapshot(WithFreshSnapshot(ctx), true)
	if err != nil {
		return nil, err
	}
	plan.snapshot = snapshot
	receipts, bases, err := snapshotImportReceipts(snapshot)
	if err != nil {
		return nil, err
	}
	intents, err := snapshotImportIntents(snapshot)
	if err != nil {
		return nil, err
	}
	captures, err := snapshotImportCaptures(snapshot)
	if err != nil {
		return nil, err
	}
	provenance, err := snapshotImportProvenance(snapshot)
	if err != nil {
		return nil, err
	}
	records := snapshot.Issues(true)
	byID := map[string]Record{}
	byOperation := map[string]Record{}
	for _, r := range records {
		if _, ok := byID[r.ID]; ok {
			return nil, fmt.Errorf("import target listing repeated %s", r.ID)
		}
		byID[r.ID] = r
		if strings.HasPrefix(r.meta.OperationID, "td-import-v1-") {
			if prior, ok := byOperation[r.meta.OperationID]; ok {
				return nil, fmt.Errorf("import identity appears in both %s and %s; resolve duplicate targets before import", prior.ID, r.ID)
			}
			byOperation[r.meta.OperationID] = r
		}
	}
	for _, e := range items {
		digest, err := e.Digest()
		if err != nil {
			return nil, err
		}
		row := ImportPlanRow{SourceID: e.Issue.ID, SourceKey: e.SourceKey(), Digest: digest, OperationID: importOperationID(e.SourceKey()), Action: "create", DependencySourceKeys: []string{}, BlockedSourceKeys: []string{}}
		if e.Issue.ParentID != "" {
			row.ParentSourceKey = e.ReferenceKey(e.Issue.ParentID)
		}
		for _, d := range e.Dependencies {
			if d.RelationType == "blocks" {
				row.BlockedSourceKeys = append(row.BlockedSourceKeys, e.ReferenceKey(d.DependsOnID))
			} else {
				row.DependencySourceKeys = append(row.DependencySourceKeys, e.ReferenceKey(d.DependsOnID))
			}
		}
		if e.GitHub != nil && strings.EqualFold(e.GitHub.Repository, c.repo) {
			r, ok := byID[e.Issue.ID]
			if !ok {
				return nil, fmt.Errorf("source %s is absent or not a task in its original repository; refusing to recreate a source number", e.Issue.ID)
			}
			row.TargetID = r.ID
			row.Observed = &r
			row.Action = "skip"
			if force {
				row.Action = "overwrite"
			}
			row.SourceChanged = !r.UpdatedAt.Equal(e.Issue.UpdatedAt) || r.Title != e.Issue.Title || r.Description != e.Issue.Description
		} else if r, ok := byOperation[row.OperationID]; ok {
			row.TargetID = r.ID
			row.Observed = &r
			row.Action = "resume"
		}
		receipt, completed := receipts[row.SourceKey]
		intent, prepared := intents[row.SourceKey]
		activeIntent := prepared && (!completed || intent.commentID > receipt.commentID)
		if capture, exists := captures[row.SourceKey]; exists && (!completed || capture.commentID > receipt.commentID) {
			if capture.Repository != c.repo || capture.TargetID != row.TargetID || capture.SourceDigest != row.Digest {
				return nil, fmt.Errorf("pending destination capture for %s requires its original source and destination", row.SourceKey)
			}
			if !activeIntent && capture.BeforeBasis != bases[row.TargetID] {
				return nil, &ConflictError{ID: row.TargetID}
			}
			if activeIntent && (capture.BeforeBasis != intent.BeforeBasis || capture.ArchiveDigest != intent.DestinationArchiveDigest) {
				return nil, fmt.Errorf("pending destination capture and prepared intent disagree for %s", row.SourceKey)
			}
			row.capture = &capture
		}
		if activeIntent {
			if row.TargetID == "" || intent.TargetID != row.TargetID {
				return nil, fmt.Errorf("prepared intent for %s disagrees with its target mapping", row.SourceKey)
			}
			state, err := intent.resumeState(row.Digest, bases[row.TargetID])
			if err != nil {
				return nil, err
			}
			row.Action, row.ResumeState = "resume", state
			row.intent = &intent
		} else if row.capture != nil {
			row.Action, row.ResumeState = "overwrite", "before_backup"
		} else if completed {
			if row.TargetID == "" || receipt.TargetID != row.TargetID {
				return nil, fmt.Errorf("completion receipt for %s disagrees with its target mapping; no import writes attempted", row.SourceKey)
			}
			if bases[row.TargetID] != receipt.TargetBasis {
				return nil, fmt.Errorf("import destination changed after completion; --force does not overwrite subsequent edits: %w", &ConflictError{ID: row.TargetID})
			}
			verified, err := verifyImportReceiptHistory(receipt, provenance)
			if err != nil {
				return nil, err
			}
			row.HistoryVerified = verified
			row.destinationArchiveDigest, row.destinationArchiveBasis = receipt.DestinationArchiveDigest, receipt.DestinationBasis
			row.SourceChanged = receipt.SourceDigest != row.Digest
			if row.SourceChanged && !force {
				return nil, fmt.Errorf("source %s changed since completed import; inspect the plan with --force before applying; no import writes attempted", row.SourceKey)
			}
			row.Action = "skip"
			if row.SourceChanged {
				row.Action = "overwrite"
			}
			if !verified {
				if row.SourceChanged {
					return nil, fmt.Errorf("%s has a fields-only completion receipt; recover history with original input before importing a changed source", row.TargetID)
				}
				row.Action, row.ResumeState = "resume_history", "history_pending"
			}
		}
		row.basis = bases[row.TargetID]
		plan.Rows = append(plan.Rows, row)
	}
	return plan, nil
}

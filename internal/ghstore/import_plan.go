package ghstore

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
)

type ImportPlanRow struct {
	SourceID                 string                `json:"source_id"`
	SourceKey                string                `json:"source_key"`
	Digest                   string                `json:"digest"`
	TargetID                 string                `json:"target_id,omitempty"`
	Action                   string                `json:"action"`
	SourceChanged            bool                  `json:"source_changed,omitempty"`
	ParentSourceKey          string                `json:"parent_source_key,omitempty"`
	DependencySourceKeys     []string              `json:"dependency_source_keys"`
	BlockedSourceKeys        []string              `json:"blocked_source_keys"`
	Observed                 *Record               `json:"-"`
	OperationID              string                `json:"operation_id"`
	ResumeState              string                `json:"resume_state,omitempty"`
	HistoryVerified          bool                  `json:"history_verified,omitempty"`
	Owner                    *ImportExecutionOwner `json:"owner,omitempty"`
	intent                   *importIntent
	receipt                  *importReceipt
	compensation             *ImportCompensation
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
	var snapshot *Snapshot
	if len(items) != 0 {
		var err error
		snapshot, err = c.ReadSnapshot(WithFreshSnapshot(ctx), true)
		if err != nil {
			return nil, err
		}
	}
	return c.planImportSnapshot(items, force, snapshot)
}

// The same source/destination contract validates a live preparation and the
// original or resulting native observations saved in a command journal.
func (c *Client) planImportSnapshot(items []ImportItem, force bool, snapshot *Snapshot) (*ImportPlan, error) {
	compensations, err := snapshotImportCompensations(snapshot, items)
	if err != nil {
		return nil, err
	}
	return c.planImportObservation(items, force, snapshot, snapshotImportIdentities(snapshot), compensations)
}

// A command journal keeps full native observations for selected destinations
// and a compact identity inventory for all original tasks. Both that reader
// and a fresh remote plan use exactly the same target and checkpoint decisions.
func (c *Client) planImportObservation(items []ImportItem, force bool, snapshot *Snapshot, identities []ImportTargetIdentity, compensations ...[]ImportCompensation) (*ImportPlan, error) {
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
	if snapshot == nil || !snapshot.fullHistory {
		return nil, fmt.Errorf("import plan requires complete native issue and comment observations")
	}
	plan.snapshot = snapshot
	var links []ImportCompensation
	if len(compensations) != 0 {
		links = compensations[0]
	}
	index, err := newImportTargetIndex(identities)
	if err != nil {
		return nil, err
	}
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
	for _, r := range records {
		if _, ok := byID[r.ID]; ok {
			return nil, fmt.Errorf("import target listing repeated %s", r.ID)
		}
		if operation, found := index.byID[r.ID]; !found || operation != r.meta.OperationID {
			return nil, fmt.Errorf("import observation %s differs from its original identity mapping", r.ID)
		}
		byID[r.ID] = r
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
		if e.AdoptTargetID != "" {
			if force {
				return nil, fmt.Errorf("explicit task adoption cannot be combined with force overwrite")
			}
			record, exists := byID[e.AdoptTargetID]
			if !exists {
				return nil, fmt.Errorf("adopted destination %s is absent or not a task", e.AdoptTargetID)
			}
			row.TargetID = record.ID
			row.Observed = &record
			row.Action = "adopt"
			row.basis = bases[row.TargetID]
			plan.Rows = append(plan.Rows, row)
			continue
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
		} else if id, ok := index.byOperation[row.OperationID]; ok {
			r, observed := byID[id]
			if !observed {
				return nil, fmt.Errorf("import destination %s is missing its original native observation", id)
			}
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
			row.Owner = intent.Owner
		} else if row.capture != nil {
			row.Action, row.ResumeState = "overwrite", "before_backup"
			row.Owner = row.capture.Owner
		} else if completed {
			row.receipt = &receipt
			if row.TargetID == "" || receipt.TargetID != row.TargetID {
				return nil, fmt.Errorf("completion receipt for %s disagrees with its target mapping; no import writes attempted", row.SourceKey)
			}
			if bases[row.TargetID] != receipt.TargetBasis {
				row.compensation, err = matchImportCompensation(c.repo, snapshot, receipt, bases[row.TargetID], links)
				if err != nil {
					return nil, fmt.Errorf("import destination changed after completion; --force does not overwrite subsequent edits: %w", err)
				}
			}
			verified, err := verifyImportReceiptHistory(receipt, provenance)
			if err != nil {
				return nil, err
			}
			row.HistoryVerified = verified
			row.Owner = receipt.Owner
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
			if row.compensation != nil {
				if !verified {
					return nil, fmt.Errorf("compensated import requires complete original source history")
				}
				row.Action = "overwrite"
				row.destinationArchiveDigest, row.destinationArchiveBasis = "", ""
			}
		}
		if row.Owner == nil && row.Observed != nil {
			row.Owner = row.Observed.meta.ImportOwner
		}
		row.basis = bases[row.TargetID]
		plan.Rows = append(plan.Rows, row)
	}
	used := map[string]string{}
	for _, row := range plan.Rows {
		if row.TargetID == "" {
			continue
		}
		if prior, exists := used[row.TargetID]; exists {
			return nil, fmt.Errorf("sources %s and %s map to the same destination %s", prior, row.SourceKey, row.TargetID)
		}
		used[row.TargetID] = row.SourceKey
	}
	return plan, nil
}

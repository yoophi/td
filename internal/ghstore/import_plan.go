package ghstore

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
)

type ImportPlanRow struct {
	SourceID             string   `json:"source_id"`
	SourceKey            string   `json:"source_key"`
	Digest               string   `json:"digest"`
	TargetID             string   `json:"target_id,omitempty"`
	Action               string   `json:"action"`
	SourceChanged        bool     `json:"source_changed,omitempty"`
	ParentSourceKey      string   `json:"parent_source_key,omitempty"`
	DependencySourceKeys []string `json:"dependency_source_keys"`
	BlockedSourceKeys    []string `json:"blocked_source_keys"`
	Observed             *Record  `json:"-"`
	OperationID          string   `json:"operation_id"`
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
}

// PlanImport validates every source before observing targets. New GitHub
// numbers cannot be allocated by dry-run; relationships use stable source keys.
func (c *Client) PlanImport(ctx context.Context, items []ImportItem, force bool) (*ImportPlan, error) {
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
	receipts, bases, err := snapshotImportReceipts(snapshot)
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
		if receipt, ok := receipts[row.SourceKey]; ok {
			if row.TargetID == "" || receipt.TargetID != row.TargetID {
				return nil, fmt.Errorf("completion receipt for %s disagrees with its target mapping; no import writes attempted", row.SourceKey)
			}
			if bases[row.TargetID] != receipt.TargetBasis {
				return nil, fmt.Errorf("import destination changed after completion; --force does not overwrite subsequent edits: %w", &ConflictError{ID: row.TargetID})
			}
			row.SourceChanged = receipt.SourceDigest != row.Digest
			if row.SourceChanged && !force {
				return nil, fmt.Errorf("source %s changed since completed import; inspect the plan with --force before applying; no import writes attempted", row.SourceKey)
			}
			row.Action = "skip"
			if row.SourceChanged {
				row.Action = "overwrite"
			}
		}
		plan.Rows = append(plan.Rows, row)
	}
	return plan, nil
}

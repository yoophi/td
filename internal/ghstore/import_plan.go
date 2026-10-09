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
	records, err := c.ListIncludingDeleted(ctx, true)
	if err != nil {
		return nil, err
	}
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
		plan.Rows = append(plan.Rows, row)
	}
	return plan, nil
}

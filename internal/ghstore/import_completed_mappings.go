package ghstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// CompletedImportMappings observes a complete native plan and accepts only
// unchanged, fully archived imports with verified completion receipts. Explicit
// adoption instead requires its original native whole-command completion; the
// retained task never gains a fabricated source-owned receipt. It does
// not accept public ImportResult counters as proof or allocate any destination.
// The resulting IDs are references, never workflow/review authority.
func (c *Client) CompletedImportMappings(ctx context.Context, items []ImportItem) (map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	plan, err := c.PlanImport(ctx, items, false)
	if err != nil {
		return nil, err
	}
	adopted, err := c.completedAdoptionMappings(ctx, items, plan)
	if err != nil {
		return nil, err
	}
	result := map[string]string{}
	used := map[string]bool{}
	for _, row := range plan.Rows {
		completed := row.Action == "skip" && !row.SourceChanged && row.HistoryVerified && row.receipt != nil || row.Action == "adopt" && adopted[row.SourceKey] == row.TargetID
		if !completed || !canonicalGroupID(row.TargetID) || used[row.TargetID] {
			return nil, fmt.Errorf("import source %s lacks unchanged, verified completion/history for its destination; inspect before workspace entity writes", row.SourceKey)
		}
		used[row.TargetID] = true
		result[row.SourceKey] = row.TargetID
	}
	return result, nil
}

// Use the existing bulk observation, not one API request per adopted task.
// Native root authorship and original actor are verified once for the command.
func (c *Client) completedAdoptionMappings(ctx context.Context, items []ImportItem, plan *ImportPlan) (map[string]string, error) {
	wanted := map[string]string{}
	for _, row := range plan.Rows {
		if row.Action == "adopt" {
			wanted[row.SourceKey] = row.TargetID
		}
	}
	if len(wanted) == 0 {
		return wanted, nil
	}
	if c.journal == nil {
		return nil, fmt.Errorf("adoption verification requires its original tracked actor")
	}
	profile, err := c.requestAPI(ctx, "GET", "user", "/user", nil, false)
	if err != nil {
		return nil, err
	}
	var actor struct{ Login string }
	if err := json.Unmarshal(profile, &actor); err != nil {
		return nil, fmt.Errorf("read adoption GitHub author: %w", err)
	}
	if actor.Login == "" {
		return nil, fmt.Errorf("adoption verification requires its original GitHub author")
	}
	original, err := findCompletedAdoptionCommand(plan.snapshot, items, c.journal.SessionID, actor.Login)
	if err != nil {
		return nil, err
	}
	if original == nil || original.ImportForward == nil {
		return nil, fmt.Errorf("adopted task mappings lack their original applied whole-command completion")
	}
	entries := map[int]snapshotEntry{}
	for _, entry := range plan.snapshot.entries {
		entries[entry.Issue.Number] = entry
	}
	for n, member := range original.ImportCommand.Members {
		if member.Action != "adopt" {
			continue
		}
		if wanted[member.SourceKey] != member.TargetID {
			return nil, fmt.Errorf("adoption binding differs from original completion")
		}
		target := original.ImportForward.Targets[n]
		entry, found := entries[target.Issue.Number]
		actual, err := canonicalImportDestination(c.repo, entry.Issue, entry.Comments)
		if err != nil {
			return nil, err
		}
		expected, err := canonicalImportDestination(c.repo, target.Issue, target.Comments)
		if err != nil {
			return nil, err
		}
		if !found || !bytes.Equal(actual, expected) {
			return nil, &ConflictError{ID: member.TargetID}
		}
	}
	return wanted, nil
}

func findCompletedAdoptionCommand(snapshot *Snapshot, items []ImportItem, session, actor string) (*ActionJournalRecord, error) {
	input, err := json.Marshal(items)
	if err != nil {
		return nil, err
	}
	history, err := snapshot.ActionHistory("")
	if err != nil {
		return nil, err
	}
	var original *ActionJournalRecord
	for i := range history {
		root := &history[i]
		if root.ImportCommand == nil || root.ImportCommand.Force || root.ImportCommand.Input != string(input) || root.Phase != "applied" || root.SessionID != session || !strings.EqualFold(root.Author, actor) {
			continue
		}
		if original != nil {
			return nil, fmt.Errorf("multiple completed adoption commands match this input; inspect original checkpoint")
		}
		original = root
	}
	return original, nil
}

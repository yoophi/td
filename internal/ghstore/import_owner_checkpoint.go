package ghstore

import (
	"fmt"
	"reflect"
	"strings"
)

type importCheckpointBinding struct {
	source, digest, target string
	owner                  *ImportExecutionOwner
}

func decodeImportCheckpointBinding(body string) (*importCheckpointBinding, error) {
	switch {
	case strings.Contains(body, importIntentPrefix):
		value, err := decodeImportIntent(body)
		if err != nil {
			return nil, err
		}
		return &importCheckpointBinding{value.SourceKey, value.SourceDigest, value.TargetID, value.Owner}, nil
	case strings.Contains(body, importCapturePrefix):
		value, err := decodeImportCapture(body)
		if err != nil {
			return nil, err
		}
		return &importCheckpointBinding{value.SourceKey, value.SourceDigest, value.TargetID, value.Owner}, nil
	case strings.Contains(body, importReceiptPrefix):
		value, err := decodeImportReceipt(body)
		if err != nil {
			return nil, err
		}
		return &importCheckpointBinding{value.SourceKey, value.SourceDigest, value.TargetID, value.Owner}, nil
	default:
		return nil, nil
	}
}

// A JSON owner alone is not authority to bypass the original target basis.
// Check native evidence from the same complete observation used by the planner,
// before any allocation, provenance or field write. No additional reads occur.
func validateImportExecutionCheckpoints(scope *importExecutionScope, plan *ImportPlan) error {
	rows := make(map[string]ImportPlanRow, len(plan.Rows))
	for _, row := range plan.Rows {
		rows[row.SourceKey] = row
	}
	baseline := map[int64]apiComment{}
	for _, member := range scope.root.ImportCommand.Members {
		if member.Before == nil {
			continue
		}
		for _, comment := range member.Before.Comments {
			binding, err := decodeImportCheckpointBinding(comment.Body)
			if err != nil {
				return fmt.Errorf("original import checkpoint %d: %w", comment.ID, err)
			}
			if binding != nil && binding.source == member.SourceKey {
				baseline[comment.ID] = comment
			}
		}
	}
	client := &Client{repo: scope.root.ImportCommand.Repository}
	for _, entry := range plan.snapshot.entries {
		for _, comment := range entry.Comments {
			if original, exists := baseline[comment.ID]; exists {
				if !reflect.DeepEqual(original, comment) {
					return fmt.Errorf("original import checkpoint %d changed; inspect it before resuming", comment.ID)
				}
				delete(baseline, comment.ID)
				continue
			}
			binding, err := decodeImportCheckpointBinding(comment.Body)
			if err != nil {
				return fmt.Errorf("import checkpoint %d: %w", comment.ID, err)
			}
			if binding == nil {
				continue
			}
			row, selected := rows[binding.source]
			if !selected {
				continue
			}
			// Older v1 or other-generation checkpoints are permissible only as
			// the exact frozen baseline above. Newly appearing legacy records
			// cannot be adopted as an owned continuation, even after allocation.
			if binding.owner == nil || *binding.owner != scope.owner || binding.digest != row.Digest || binding.target != row.TargetID {
				return fmt.Errorf("import checkpoint %d is outside command %s's original baseline and execution ownership", comment.ID, scope.root.ID)
			}
			number, err := client.commentIssueNumber(comment.IssueURL)
			if err != nil || number != entry.Issue.Number || binding.target != fmt.Sprintf("gh-%d", number) || !validJournalPartNative(comment) || !strings.EqualFold(comment.User.Login, scope.root.Author) || comment.CreatedAt.Before(scope.root.CreatedAt) {
				return fmt.Errorf("import checkpoint %d has unverified native author, target or clocks for command %s", comment.ID, scope.root.ID)
			}
		}
	}
	if len(baseline) != 0 {
		return fmt.Errorf("original import checkpoint is missing; inspect the frozen baseline before resuming")
	}
	return nil
}

// A tracked new command may supersede a completed source version. This is not
// permission for the untracked executor to adopt owned pending work: the old
// command must be complete and its checkpoint evidence must still be exact.
func validateNewImportCommandPlan(plan *ImportPlan, items []ImportItem, force bool) error {
	if _, err := selectPendingImportExecution(plan.snapshot, plan, items, force, "", ""); err != nil {
		return err
	}
	history, err := plan.snapshot.ActionHistory("")
	if err != nil {
		return err
	}
	roots := map[string]ActionJournalRecord{}
	completedSources := map[string]ActionJournalRecord{}
	for _, root := range history {
		roots[root.ID] = root
		if root.Phase != "applied" || root.ImportForward == nil {
			continue
		}
		// ActionHistory is newest first. A skipped member only observes an
		// earlier source version; it does not acquire that version's ownership.
		for _, member := range root.ImportCommand.Members {
			if _, exists := completedSources[member.SourceKey]; !exists && member.Action != "skip" && member.Action != "adopt" {
				completedSources[member.SourceKey] = root
			}
		}
	}
	sources := map[string]ImportItem{}
	for _, item := range items {
		sources[item.SourceKey()] = item
	}
	entries := map[string]snapshotEntry{}
	for _, entry := range plan.snapshot.entries {
		entries[fmt.Sprintf("gh-%d", entry.Issue.Number)] = entry
	}
	for _, row := range plan.Rows {
		if row.Action == "skip" || row.Action == "adopt" {
			continue
		}
		if row.compensation != nil {
			previous, found := roots[row.compensation.CommandID]
			if !found || previous.ImportCommand == nil || previous.Phase != "undone" {
				return fmt.Errorf("original import compensation disappeared before reimport")
			}
			verified := false
			for n, member := range previous.ImportCommand.Members {
				if member.SourceKey != row.SourceKey {
					continue
				}
				link, err := importCompensationFromRoot(&previous, n)
				if err != nil {
					return err
				}
				verified = reflect.DeepEqual(link, row.compensation)
			}
			if !verified {
				return fmt.Errorf("reimport compensation no longer matches its completed original command")
			}
			continue
		}
		// Source history remains authoritative even if live ownership metadata
		// was removed. The allocation owner may belong to a different source,
		// such as a legacy task later exported and restored in its native repo.
		previous, found := completedSources[row.SourceKey]
		if !found {
			item := sources[row.SourceKey]
			nativeRestore := item.GitHub != nil && strings.EqualFold(item.GitHub.Repository, plan.Repository) && item.Issue.ID == row.TargetID
			if row.Owner == nil {
				continue
			}
			previous, found = roots[row.Owner.CommandID]
			if !found || previous.Phase != "applied" || previous.ImportForward == nil || previous.OperationID != row.Owner.OperationID || previous.SessionID != row.Owner.SessionID {
				return fmt.Errorf("%s has no valid completed import baseline; inspect command %s before starting another execution", row.TargetID, row.Owner.CommandID)
			}
			if nativeRestore && row.intent == nil && row.capture == nil && row.receipt == nil {
				ownsAllocation := false
				for n, member := range previous.ImportCommand.Members {
					ownsAllocation = ownsAllocation || member.Action == "create" && fmt.Sprintf("gh-%d", previous.ImportForward.Targets[n].Issue.Number) == row.TargetID
				}
				if !ownsAllocation {
					return fmt.Errorf("previous import does not own this native allocation")
				}
				continue
			}
		}
		var original *ImportNativeState
		for n, member := range previous.ImportCommand.Members {
			if member.SourceKey == row.SourceKey && fmt.Sprintf("gh-%d", previous.ImportForward.Targets[n].Issue.Number) == row.TargetID {
				original = &previous.ImportForward.Targets[n]
			}
		}
		if original == nil {
			return fmt.Errorf("previous import does not own this source/destination")
		}
		basis, err := importTargetBasis(original.Issue)
		if err != nil || basis != row.basis {
			return &ConflictError{ID: row.TargetID}
		}
		baseline := map[int64]apiComment{}
		for _, comment := range original.Comments {
			binding, err := decodeImportCheckpointBinding(comment.Body)
			if err != nil {
				return err
			}
			if binding != nil && binding.source == row.SourceKey {
				baseline[comment.ID] = comment
			}
		}
		for _, comment := range entries[row.TargetID].Comments {
			binding, err := decodeImportCheckpointBinding(comment.Body)
			if err != nil {
				return err
			}
			original, exists := baseline[comment.ID]
			if exists {
				if !reflect.DeepEqual(original, comment) {
					return fmt.Errorf("previous import checkpoint changed before the new command")
				}
				delete(baseline, comment.ID)
			} else if binding != nil && binding.source == row.SourceKey {
				return fmt.Errorf("new checkpoint is outside the previous completed import")
			}
		}
		if len(baseline) != 0 {
			return fmt.Errorf("previous import checkpoint disappeared before the new command")
		}
	}
	return nil
}

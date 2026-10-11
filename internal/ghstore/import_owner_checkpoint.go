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

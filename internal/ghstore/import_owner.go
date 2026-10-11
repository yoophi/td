package ghstore

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// Source identity survives multiple imports; execution ownership does not.
// The command ID is a real native journal comment, never a future allocation.
type ImportExecutionOwner struct {
	CommandID   string `json:"command_id"`
	OperationID string `json:"operation_id"`
	SessionID   string `json:"session_id"`
}

func (o *ImportExecutionOwner) validate() error {
	if o == nil {
		return fmt.Errorf("import execution owner is required")
	}
	n, err := CommentNumber(o.CommandID)
	if err != nil || o.CommandID != fmt.Sprintf("ghc-%d", n) {
		return fmt.Errorf("import owner requires a canonical native command ID")
	}
	probe := ActionJournal{OperationID: o.OperationID, SessionID: o.SessionID, IssueID: "gh-1", Action: "create", Phase: "prepared"}
	return probe.validate()
}

func validateImportCheckpointOwner(version int, owner *ImportExecutionOwner) error {
	if version == 1 && owner == nil {
		return nil
	}
	if version != 2 || owner == nil {
		return fmt.Errorf("import checkpoint version and execution owner disagree")
	}
	return owner.validate()
}

func importCheckpointStart(prefix string, version int) string {
	return fmt.Sprintf("%sv%d\n", prefix, version)
}

func importCheckpointJSON(body, prefix string) ([]byte, error) {
	index := strings.Index(body, prefix)
	if index < 0 {
		return nil, nil
	}
	block := strings.TrimSpace(body[index:])
	for _, version := range []int{1, 2} {
		start := importCheckpointStart(prefix, version)
		if strings.Count(body, prefix) == 1 && strings.HasPrefix(block, start) && strings.HasSuffix(block, markerEnd) {
			return []byte(strings.TrimSuffix(strings.TrimPrefix(block, start), markerEnd)), nil
		}
	}
	return nil, fmt.Errorf("malformed or unsupported import checkpoint")
}

type importExecutionContextKey struct{}
type importExecutionScope struct {
	root  *ActionJournalRecord
	owner ImportExecutionOwner
}

func importExecution(ctx context.Context) *importExecutionScope {
	scope, _ := ctx.Value(importExecutionContextKey{}).(*importExecutionScope)
	return scope
}

func withImportExecution(ctx context.Context, root *ActionJournalRecord, session, actor string) (context.Context, error) {
	if root == nil || root.ImportCommand == nil || root.Phase != "prepared" || root.SessionID != session || actor == "" || !strings.EqualFold(root.Author, actor) || root.CreatedAt.IsZero() || root.UpdatedAt.Before(root.CreatedAt) {
		return ctx, fmt.Errorf("import resume requires its original prepared command, session and GitHub author")
	}
	if err := root.validate(); err != nil {
		return ctx, err
	}
	owner := ImportExecutionOwner{CommandID: root.ID, OperationID: root.OperationID, SessionID: root.SessionID}
	if err := owner.validate(); err != nil {
		return ctx, err
	}
	// A caller cannot replace a root's original input while an API call runs.
	frozen := *root
	var err error
	frozen.ActionJournal, err = copyCreationJournal(root.ActionJournal)
	if err != nil {
		return ctx, err
	}
	return context.WithValue(ctx, importExecutionContextKey{}, &importExecutionScope{root: &frozen, owner: owner}), nil
}

func importCheckpointOwnership(ctx context.Context, prior *ImportExecutionOwner) (int, *ImportExecutionOwner, error) {
	scope := importExecution(ctx)
	if scope == nil {
		if prior != nil {
			return 0, nil, fmt.Errorf("owned import checkpoint requires command %s and session %s", prior.CommandID, prior.SessionID)
		}
		return 1, nil, nil
	}
	if prior != nil && *prior != scope.owner {
		return 0, nil, fmt.Errorf("import cannot replace another command's checkpoint ownership")
	}
	owner := scope.owner
	return 2, &owner, nil
}

// Read-only discovery uses the already complete bulk observation. A partial
// run keeps its original root/input; overlap cannot silently create a new run.
func selectPendingImportExecution(snapshot *Snapshot, plan *ImportPlan, items []ImportItem, force bool, session, actor string) (*ActionJournalRecord, error) {
	if snapshot == nil || !snapshot.fullHistory {
		return nil, fmt.Errorf("import ownership requires a complete snapshot")
	}
	history, err := snapshot.ActionHistory("")
	if err != nil {
		return nil, err
	}
	sources, targets, allocations := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, row := range plan.Rows {
		sources[row.SourceKey], targets[row.TargetID] = true, row.TargetID != ""
		if row.Observed != nil && row.Observed.meta.OperationID != "" {
			allocations[row.Observed.meta.OperationID] = true
		}
	}
	var selected *ActionJournalRecord
	for n := range history {
		root := &history[n]
		if root.ImportCommand == nil || root.Phase != "prepared" {
			continue
		}
		overlap := false
		for _, member := range root.ImportCommand.Members {
			overlap = overlap || sources[member.SourceKey] || member.TargetID != "" && targets[member.TargetID]
			// A prepared creation has no target number yet. Its stable allocation
			// identity still binds the real issue if another source exports it.
			overlap = overlap || member.Action == "create" && allocations[member.OperationID]
		}
		if !overlap {
			continue
		}
		if selected != nil {
			return nil, fmt.Errorf("overlapping pending import commands %s and %s; inspect them before any import write", selected.ID, root.ID)
		}
		selected = root
	}
	if selected == nil {
		return nil, nil
	}
	input, err := json.Marshal(items)
	if err != nil {
		return nil, err
	}
	if selected.ImportCommand.Input != string(input) || selected.ImportCommand.Force != force {
		return nil, fmt.Errorf("pending import %s requires its complete original input and force selection", selected.ID)
	}
	if selected.SessionID != session || actor == "" || !strings.EqualFold(selected.Author, actor) {
		return nil, fmt.Errorf("pending import %s belongs to session %s and GitHub author %s; observation/reconciliation does not transfer resume or undo ownership", selected.ID, selected.SessionID, selected.Author)
	}
	return selected, nil
}

func (c *Client) fenceImportExecution(ctx context.Context) error {
	scope := importExecution(ctx)
	if scope == nil {
		return nil
	}
	n, _ := CommentNumber(scope.root.ID)
	current, err := c.readJournalReceipt(ctx, fmt.Sprintf("/issues/comments/%d", n), scope.root)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(current, scope.root) {
		return fmt.Errorf("original import command changed; no further import target write attempted")
	}
	return nil
}

func validateImportExecutionPlan(ctx context.Context, plan *ImportPlan, items []ImportItem, force bool) error {
	if len(plan.Rows) == 0 {
		return nil
	}
	scope := importExecution(ctx)
	if scope == nil {
		if _, err := selectPendingImportExecution(plan.snapshot, plan, items, force, "", ""); err != nil {
			return err
		}
		for _, row := range plan.Rows {
			if row.Action != "skip" && row.Owner != nil {
				return fmt.Errorf("%s is owned by import %s in session %s; use its tracked command before resuming", row.TargetID, row.Owner.CommandID, row.Owner.SessionID)
			}
		}
		return nil
	}
	if len(plan.Rows) != len(scope.root.ImportCommand.Members) {
		return fmt.Errorf("import replan lost original membership")
	}
	selected, err := selectPendingImportExecution(plan.snapshot, plan, items, force, scope.owner.SessionID, scope.root.Author)
	if err != nil {
		return err
	}
	if selected == nil || !reflect.DeepEqual(selected, scope.root) {
		return fmt.Errorf("original import command is missing or changed in the fresh observation")
	}
	return validateImportExecutionRows(scope, plan)
}

// Shared by live replans and the completed command's native evidence reader.
func validateImportExecutionRows(scope *importExecutionScope, plan *ImportPlan) error {
	if err := validateImportExecutionCheckpoints(scope, plan); err != nil {
		return err
	}
	originalTargets, err := newImportTargetIndex(scope.root.ImportCommand.OriginalTargets)
	if err != nil {
		return err
	}
	for n, row := range plan.Rows {
		original := scope.root.ImportCommand.Members[n]
		if original.SourceKey != row.SourceKey || original.Digest != row.Digest || original.TargetID != "" && original.TargetID != row.TargetID {
			return fmt.Errorf("import replan changed original source/destination membership")
		}
		if row.intent != nil && row.intent.Owner != nil && *row.intent.Owner != scope.owner || row.capture != nil && row.capture.Owner != nil && *row.capture.Owner != scope.owner {
			return fmt.Errorf("import cannot resume another command's pending checkpoint")
		}
		if original.Before != nil {
			basis, err := importTargetBasis(original.Before.Issue)
			if err != nil {
				return err
			}
			owned := row.intent != nil && row.intent.Owner != nil && *row.intent.Owner == scope.owner || row.capture != nil && row.capture.Owner != nil && *row.capture.Owner == scope.owner || row.Action == "skip" && row.Owner != nil && *row.Owner == scope.owner
			if !owned && row.basis != basis {
				return &ConflictError{ID: row.TargetID}
			}
		}
		if original.Action != "skip" && row.Action == "skip" && (row.Owner == nil || *row.Owner != scope.owner) {
			return fmt.Errorf("import completion belongs to another execution")
		}
		if original.Action == "create" && row.TargetID != "" {
			if _, existed := originalTargets.byID[row.TargetID]; existed {
				return fmt.Errorf("allocated source %s uses a target that existed before command %s", row.SourceKey, scope.root.ID)
			}
			if row.Observed == nil || row.Observed.meta.ImportOwner == nil || *row.Observed.meta.ImportOwner != scope.owner {
				return fmt.Errorf("allocated source %s is not owned by original import %s", row.SourceKey, scope.root.ID)
			}
			for _, entry := range plan.snapshot.entries {
				if fmt.Sprintf("gh-%d", entry.Issue.Number) == row.TargetID && (!strings.EqualFold(entry.Issue.User.Login, scope.root.Author) || entry.Issue.CreatedAt.Before(scope.root.CreatedAt)) {
					return fmt.Errorf("import allocation has a different native author or predates its command")
				}
			}
		}
	}
	return nil
}

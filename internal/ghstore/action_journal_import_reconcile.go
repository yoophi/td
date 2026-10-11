package ghstore

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"time"
)

func (c *Client) reconcileImportCommand(ctx context.Context, root *ActionJournalRecord, session, actor string, apply bool) (*ActionReconcileResult, error) {
	if root.ImportInverse != nil {
		return c.reconcileImportInverse(ctx, root, session, actor, apply)
	}
	items, err := root.ImportCommand.inputItems()
	if err != nil {
		return nil, err
	}
	plan, err := c.planImport(ctx, items, root.ImportCommand.Force)
	if err != nil {
		return nil, err
	}
	result := &ActionReconcileResult{Journal: root, ImportPlan: plan}
	if root.Phase == "applied" {
		if err := c.verifyCompletedImport(ctx, root, plan.snapshot); err != nil {
			return nil, err
		}
		result.AlreadyReconciled = true
		return result, nil
	}
	// The original scope is used only to validate its evidence. Observation is
	// attributed to the actual caller below, and never invokes executeImport.
	ctx, err = withImportExecution(ctx, root, root.SessionID, actor)
	if err != nil {
		return nil, err
	}
	if err := validateImportExecutionPlan(ctx, plan, items, root.ImportCommand.Force); err != nil {
		return nil, err
	}
	for _, row := range plan.Rows {
		result.Partial = result.Partial || row.Action != "skip"
	}
	if result.Partial {
		return result, nil
	}
	observation := CreationObservation{Proof: "observed", SessionID: session, Actor: actor, ObservedAt: time.Now().UTC()}
	forward, err := c.observeImportCompletion(ctx, root, items, root.ImportCommand.Force, observation)
	if err != nil {
		return nil, err
	}
	result.ForwardOutcome = &ActionUndoOutcome{Proof: "import-contents-observed"}
	if !apply {
		return result, nil
	}
	result.Journal, err = c.completeImportCommand(ctx, root, forward)
	if err != nil {
		return nil, err
	}
	result.Applied = true
	return result, nil
}

func (c *Client) verifyCompletedImport(ctx context.Context, root *ActionJournalRecord, snapshot *Snapshot) error {
	history, err := snapshot.ActionHistory("")
	if err != nil {
		return err
	}
	found := false
	for _, candidate := range history {
		if candidate.ID == root.ID {
			found = reflect.DeepEqual(candidate, *root)
		}
	}
	if !found || root.ImportForward == nil {
		return fmt.Errorf("original import completion changed or disappeared")
	}
	entries := map[int]snapshotEntry{}
	for _, entry := range snapshot.entries {
		entries[entry.Issue.Number] = entry
	}
	for _, target := range root.ImportForward.Targets {
		entry, found := entries[target.Issue.Number]
		// Optional API raw JSON fields can be omitted on GET and represented
		// as null in archived JSON. Compare canonical native bytes, retaining
		// every field, comment and clock rather than Go nil representations.
		actual, err := canonicalImportDestination(c.repo, entry.Issue, entry.Comments)
		if err != nil {
			return err
		}
		expected, err := canonicalImportDestination(c.repo, target.Issue, target.Comments)
		if err != nil {
			return err
		}
		if !found || !bytes.Equal(actual, expected) {
			return &ConflictError{ID: fmt.Sprintf("gh-%d", target.Issue.Number)}
		}
		events, err := c.readStateEvents(ctx, fmt.Sprintf("gh-%d", target.Issue.Number))
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(events, target.Events) {
			return fmt.Errorf("import native events changed after completion")
		}
	}
	return nil
}

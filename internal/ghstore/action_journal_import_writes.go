package ghstore

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"reflect"
	"time"
)

func (c *Client) importCommand(ctx context.Context, input []ImportItem, force bool) (*ImportResult, error) {
	if c.journal.Group != nil || importExecution(ctx) != nil {
		return nil, fmt.Errorf("import requires an independent tracked command")
	}
	items, err := freezeImportItems(input)
	if err != nil {
		return nil, err
	}
	plan, err := c.planImport(ctx, items, force)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		plan.DryRun = false
		return &ImportResult{Plan: plan}, nil
	}
	profile, err := c.requestAPI(ctx, "GET", "user", "/user", nil, false)
	if err != nil {
		return nil, err
	}
	var actor struct{ Login string }
	if err := json.Unmarshal(profile, &actor); err != nil || actor.Login == "" {
		return nil, fmt.Errorf("import requires a verified GitHub actor")
	}
	session := c.journal.SessionID
	root, err := selectPendingImportExecution(plan.snapshot, plan, items, force, session, actor.Login)
	if err != nil {
		return nil, err
	}
	resumed := root != nil
	if root == nil {
		changes := false
		for _, row := range plan.Rows {
			changes = changes || row.Action != "skip"
		}
		if !changes {
			plan.DryRun = false
			return &ImportResult{Plan: plan, Skipped: len(plan.Rows)}, nil
		}
		if err := validateNewImportCommandPlan(plan, items, force); err != nil {
			return nil, err
		}
		events, err := c.importPlanEvents(ctx, plan)
		if err != nil {
			return nil, err
		}
		command, err := prepareImportCommand(items, force, plan, events)
		if err != nil {
			return nil, err
		}
		// A private unused carrier permits capacity validation before bootstrap.
		used := map[string]bool{}
		for _, identity := range command.OriginalTargets {
			used[identity.IssueID] = true
		}
		carrier := 1
		for used[fmt.Sprintf("gh-%d", carrier)] {
			carrier++
		}
		a := ActionJournal{OperationID: "td-op-" + rand.Text(), SessionID: session, CarrierID: fmt.Sprintf("gh-%d", carrier), Action: "import", Phase: "prepared", ImportCommand: command}
		if _, err := planActionJournalPublication(a); err != nil {
			return nil, err
		}
		if err := c.EnableActionJournal(ctx, session); err != nil {
			return nil, err
		}
		a.CarrierID = c.journal.Carrier.ID
		publication, err := planActionJournalPublication(a)
		if err != nil {
			return nil, err
		}
		fence := func() error { return c.verifyImportPreparation(ctx, command, items, force) }
		root, err = c.publishJournal(ctx, fmt.Sprintf("/issues/%d/comments", c.journal.Carrier.Number), publication, nil, fence)
		if err != nil {
			return nil, fmt.Errorf("import command publication %s is uncertain; inspect remote history before retrying; no import target write attempted: %w", a.OperationID, err)
		}
	}
	ctx, err = withImportExecution(ctx, root, session, actor.Login)
	if err != nil {
		return nil, err
	}
	// Existing per-source checkpoints belong to this root. Scalar creation or
	// update journals would split the command and misattribute whole undo.
	worker := *c
	worker.journal = nil
	result, err := worker.executeImport(ctx, items, force)
	if err == nil {
		proof := "verified"
		if resumed {
			proof = "observed"
		}
		var forward *ActionImportForward
		forward, err = c.observeImportCompletion(ctx, root, items, force, CreationObservation{Proof: proof, SessionID: session, Actor: actor.Login, ObservedAt: time.Now().UTC()})
		if err == nil {
			_, err = c.completeImportCommand(ctx, root, forward)
		}
	}
	if err != nil {
		return result, fmt.Errorf("import command %s (operation %s, session %s) is incomplete; earlier writes remain; inspect and explicitly resume with the original input; no automatic retry: %w", root.ID, root.OperationID, root.SessionID, err)
	}
	return result, nil
}

func (c *Client) importPlanEvents(ctx context.Context, plan *ImportPlan) (map[string][]nativeStateEvent, error) {
	events := map[string][]nativeStateEvent{}
	for _, row := range plan.Rows {
		if row.TargetID == "" {
			continue
		}
		observed, err := c.readStateEvents(ctx, row.TargetID)
		if err != nil {
			return nil, err
		}
		events[row.TargetID] = observed
		if row.compensation != nil && stateEventDigest(observed) != row.compensation.EventsSHA256 {
			return nil, fmt.Errorf("import compensation native events changed before reimport: %w", &ConflictError{ID: row.TargetID})
		}
	}
	return events, nil
}

func (c *Client) verifyImportPreparation(ctx context.Context, original *ActionImportCommand, items []ImportItem, force bool) error {
	plan, err := c.planImport(ctx, items, force)
	if err != nil {
		return err
	}
	if err := validateNewImportCommandPlan(plan, items, force); err != nil {
		return err
	}
	events, err := c.importPlanEvents(ctx, plan)
	if err != nil {
		return err
	}
	current, err := prepareImportCommand(items, force, plan, events)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(original.Members, current.Members) {
		return fmt.Errorf("import destinations changed while preparing the original command; no target write attempted")
	}
	return nil
}

func (c *Client) observeImportCompletion(ctx context.Context, root *ActionJournalRecord, items []ImportItem, force bool, observation CreationObservation) (*ActionImportForward, error) {
	plan, err := c.planImport(ctx, items, force)
	if err != nil {
		return nil, err
	}
	if err := validateImportExecutionPlan(ctx, plan, items, force); err != nil {
		return nil, err
	}
	for _, row := range plan.Rows {
		if row.Action != "skip" {
			return nil, fmt.Errorf("import %s has unfinished source %s; observation does not replay missing effects", root.ID, row.SourceKey)
		}
	}
	events, err := c.importPlanEvents(ctx, plan)
	if err != nil {
		return nil, err
	}
	entries := map[string]snapshotEntry{}
	for _, entry := range plan.snapshot.entries {
		entries[fmt.Sprintf("gh-%d", entry.Issue.Number)] = entry
	}
	forward := &ActionImportForward{Observation: observation, Targets: []ImportNativeState{}}
	for _, row := range plan.Rows {
		entry := entries[row.TargetID]
		forward.Targets = append(forward.Targets, ImportNativeState{Issue: entry.Issue, Comments: append([]apiComment{}, entry.Comments...), Events: events[row.TargetID]})
	}
	return forward, nil
}

func (c *Client) completeImportCommand(ctx context.Context, root *ActionJournalRecord, forward *ActionImportForward) (*ActionJournalRecord, error) {
	if root.ImportCommand == nil || root.Phase != "prepared" || root.ImportForward != nil {
		return nil, fmt.Errorf("import completion requires its original prepared command")
	}
	next, err := copyCreationJournal(root.ActionJournal)
	if err != nil {
		return nil, err
	}
	next.Phase, next.ImportForward = "applied", forward
	publication, err := planActionJournalPublication(next)
	if err != nil {
		return nil, err
	}
	items, err := root.ImportCommand.inputItems()
	if err != nil {
		return nil, err
	}
	fence := func() error {
		observed, err := c.observeImportCompletion(ctx, root, items, root.ImportCommand.Force, forward.Observation)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(observed, forward) {
			return fmt.Errorf("import result changed before command completion")
		}
		return nil
	}
	n, _ := CommentNumber(root.ID)
	return c.publishJournal(ctx, fmt.Sprintf("/issues/comments/%d", n), publication, root, fence)
}

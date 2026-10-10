package ghstore

import (
	"context"
	"fmt"
	"slices"

	"github.com/marcus/td/internal/models"
)

func plannedTaskCommand(command string) bool {
	return slices.Contains([]string{"update", "reject", "delete", "restore"}, command)
}

func (g *ActionGroup) hasPlannedInputs() bool { return g.Planned || g.Command == "update" }

func (a ActionJournal) validatePlannedTaskMember(m ActionGroupMember) error {
	g := a.Group
	if g.hasPlannedInputs() != (m.Input != nil) || m.Noop && !g.Planned {
		return fmt.Errorf("planned group requires every full input and explicit no-op scope")
	}
	if m.Input == nil {
		return nil
	}
	if g.Ordered != nil || len(m.Input.Related) != 0 {
		return fmt.Errorf("independent planned task updates cannot declare ordered or related effects")
	}
	probe := ActionJournal{Action: "update", IssueID: m.IssueID, CarrierID: a.CarrierID,
		SessionID: a.SessionID, Before: &m.Before, UpdateIntent: m.Input}
	if err := probe.validateUpdateIntent(); err != nil {
		return err
	}
	after, err := m.Input.Target.contents()
	if err != nil {
		return err
	}
	if m.Noop {
		before, err := actionStateRecord(&m.Before, m.IssueID)
		if err != nil {
			return err
		}
		valid := g.Command == "update" || g.Command == "reject" && before.Status == models.StatusOpen ||
			g.Command == "delete" && before.DeletedAt != nil || g.Command == "restore" && before.DeletedAt == nil
		if !valid || !sameActionContents(m.Before, after) {
			return fmt.Errorf("declared no-op does not match observed command state")
		}
		return nil
	}
	probe.After = &after
	return plannedTaskEffectScope(g.Command, &ActionJournalRecord{ActionJournal: probe})
}

func plannedTaskEffectScope(command string, a *ActionJournalRecord) error {
	if command == "update" {
		return scalarFieldUndoScope(a)
	}
	var matched bool
	var err error
	switch command {
	case "reject":
		matched, err = reviewTransitionUndoScope(a)
	case "delete", "restore":
		matched, err = deletionUndoScope(a)
	default:
		return fmt.Errorf("unknown planned task command")
	}
	if err != nil {
		return err
	}
	if !matched {
		return &UndoScopeError{Reason: "planned input differs from its declared task command"}
	}
	_, meta, _, err := decodeBody(a.After.Body)
	if err != nil || meta.Details == nil || len(meta.Details.Transitions) == 0 || meta.Details.Transitions[len(meta.Details.Transitions)-1].Action != command {
		return fmt.Errorf("planned transition differs from declared command")
	}
	return nil
}

// BeginPlannedCommand validates and freezes all independent command inputs,
// including explicit no-ops, before bootstrap or any task write. The existing
// command loop uses ApplyPlannedCommandMember and CompleteActionGroup so local
// focus and output handling remain tied to actual completed effects.
func (c *Client) BeginPlannedCommand(ctx context.Context, command string, ids []string, options TransitionOptions) error {
	if len(ids) < 2 || c.journal == nil || c.journal.Group != nil || c.journal.SessionID != options.SessionID || !slices.Contains([]string{"reject", "delete", "restore"}, command) {
		return fmt.Errorf("planned command needs a matching tracked session, multiple targets and no active group; no write attempted")
	}
	records, err := c.selectPlannedTasks(ctx, ids, command != "reject")
	if err != nil {
		return err
	}
	plans := make([]*issueUpdatePlan, 0, len(records))
	for i := range records {
		var plan *issueUpdatePlan
		if command == "reject" {
			plan, err = c.prepareRejection(ctx, &records[i], options)
		} else {
			plan, err = c.prepareDeletion(ctx, &records[i], command == "delete", options.SessionID, options.Reason)
		}
		if err != nil {
			return fmt.Errorf("plan %s %s (no write attempted): %w", command, records[i].ID, err)
		}
		plans = append(plans, plan)
	}
	return c.beginPlannedTaskGroup(ctx, command, plans, true)
}

func (c *Client) ApplyPlannedCommandMember(ctx context.Context, id string) (*Record, bool, error) {
	if c.journal == nil || c.journal.Group == nil || !c.journal.Group.Journal.Group.Planned {
		return nil, false, fmt.Errorf("an active planned command is required")
	}
	n, err := Number(id)
	if err != nil {
		return nil, false, err
	}
	id = fmt.Sprintf("gh-%d", n)
	active := c.journal.Group
	plan, found := active.Plans[id]
	if !found || active.Attempted[id] {
		return nil, false, fmt.Errorf("planned target %s was not selected or was already attempted; no target write attempted", id)
	}
	if plan.noop {
		if err := c.verifyPlannedUpdateGroup(ctx, nil); err != nil {
			return nil, false, err
		}
		active.Attempted[id] = true
		return plan.observed, true, nil
	}
	result, err := c.writeIssueUpdate(ctx, plan)
	if err != nil {
		return nil, false, fmt.Errorf("planned group %s stopped at %s; inspect and reconcile before retrying (earlier changes remain): %w", active.Journal.ID, id, err)
	}
	return result, false, nil
}

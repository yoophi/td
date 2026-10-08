package ghstore

import (
	"context"
	"crypto/rand"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/marcus/td/internal/models"
)

// Watch the ancestor chain and every direct child used to decide completion.
// These are optimistic, paginated reads, not a GitHub transaction or lock.
func (c *Client) parentGraph(ctx context.Context, root string) (map[string]Record, []string, error) {
	records, err := c.listWithRoot(ctx, root)
	if err != nil {
		return nil, nil, err
	}
	all := make(map[string]Record, len(records))
	for _, record := range records {
		if _, exists := all[record.ID]; exists {
			return nil, nil, fmt.Errorf("GitHub listing repeated %s; retry the read", record.ID)
		}
		all[record.ID] = record
	}
	current, exists := all[root]
	if !exists {
		return nil, nil, fmt.Errorf("issue %s is missing or deleted", root)
	}
	graph := map[string]Record{root: current}
	parents := []string{}
	for current.ParentID != "" {
		id := current.ParentID
		if _, visited := graph[id]; visited {
			return nil, nil, fmt.Errorf("parent cycle involving %s", id)
		}
		current, exists = all[id]
		if !exists {
			parent, err := c.Get(ctx, id)
			if err != nil {
				return nil, nil, fmt.Errorf("parent %s: %w", id, err)
			}
			current = *parent
		}
		graph[id] = current
		parents = append(parents, id)
	}
	for _, record := range records {
		if slices.Contains(parents, record.ParentID) {
			graph[record.ID] = record
		}
	}
	return graph, parents, nil
}

func (c *Client) verifyParentGraph(ctx context.Context, root string, expected map[string]Record, parents []string) error {
	actual, chain, err := c.parentGraph(ctx, root)
	if err != nil {
		return err
	}
	if !slices.Equal(chain, parents) || len(actual) != len(expected) {
		return fmt.Errorf("parent/child membership changed for %s: %w", root, &ConflictError{ID: root, AfterWrite: true})
	}
	for id, previous := range expected {
		current, exists := actual[id]
		if !exists || previous.revision != current.revision {
			return &ConflictError{ID: id, AfterWrite: true}
		}
	}
	return nil
}

func (c *Client) transitionWithParents(ctx context.Context, id, action string, o TransitionOptions) (*Record, bool, error) {
	if err := ValidateReviewOptions(action, o); err != nil {
		return nil, false, err
	}
	n, err := Number(id)
	if err != nil {
		return nil, false, err
	}
	rootID := fmt.Sprintf("gh-%d", n)
	graph, parents, err := c.parentGraph(ctx, rootID)
	if err != nil {
		return nil, false, err
	}
	root := graph[rootID]
	if o.expectedRevision != nil && root.revision != *o.expectedRevision {
		return nil, false, &ConflictError{ID: rootID}
	}
	o.expectedRevision = &root.revision
	result, noop, err := c.transitionWithLocalCascades(ctx, rootID, action, o)
	if err != nil {
		return nil, false, err
	}
	completed := []string{rootID}
	updateGraph := func(record Record) {
		if _, watched := graph[record.ID]; watched {
			graph[record.ID] = record
		}
	}
	updateGraph(*result)
	for _, record := range result.CascadedReviews {
		updateGraph(record)
		completed = append(completed, record.ID)
	}
	for _, record := range result.AutoUnblocked {
		updateGraph(record)
		completed = append(completed, record.ID)
	}
	fail := func(err error) (*Record, bool, error) {
		return nil, false, fmt.Errorf("%s saved for %s; parent cascade stopped (earlier changes remain; inspect current state before retrying): %w", action, strings.Join(completed, ", "), err)
	}
	if err := c.verifyParentGraph(ctx, rootID, graph, parents); err != nil {
		return fail(err)
	}
	target := result.Status
	if target != models.StatusInReview && target != models.StatusClosed {
		return result, noop, nil
	}
	for _, id := range parents {
		parent := graph[id]
		if parent.Type != models.TypeEpic || parent.Status == target || parent.Status == models.StatusClosed {
			break
		}
		ready, children := true, 0
		for _, record := range graph {
			if record.ParentID != id {
				continue
			}
			children++
			if record.Status != target && (target != models.StatusInReview || record.Status != models.StatusClosed) {
				ready = false
				break
			}
		}
		if !ready || children == 0 {
			break
		}
		var depGraph map[string]Record
		var dependents []string
		if target == models.StatusClosed {
			depGraph, dependents, err = c.dependentGraph(ctx, id)
			if err != nil {
				return fail(err)
			}
			if depGraph[id].revision != parent.revision {
				return fail(&ConflictError{ID: id, AfterWrite: true})
			}
		}
		// Dependencies/events may take time to read. Recheck sibling/ancestor
		// observations immediately before this write as well as after it.
		updated, err := c.autoParentStatus(ctx, &parent, target, o.SessionID, func() error { return c.verifyParentGraph(ctx, rootID, graph, parents) })
		if err != nil {
			return fail(fmt.Errorf("%s: %w", id, err))
		}
		updateGraph(*updated)
		completed = append(completed, id)
		result.ParentStatusUpdates = append(result.ParentStatusUpdates, *updated)
		if err := c.verifyParentGraph(ctx, rootID, graph, parents); err != nil {
			return fail(err)
		}
		if target == models.StatusClosed {
			unblocked, _, err := c.unblockDependents(ctx, updated, false, "close", o, depGraph, dependents)
			if err != nil {
				return fail(err)
			}
			for _, record := range unblocked.AutoUnblocked {
				updateGraph(record)
				completed = append(completed, record.ID)
				result.AutoUnblocked = append(result.AutoUnblocked, record)
			}
			if err := c.verifyParentGraph(ctx, rootID, graph, parents); err != nil {
				return fail(err)
			}
		}
	}
	return result, noop && len(result.ParentStatusUpdates) == 0, nil
}

// An epic reaching completion through its children is an automatic aggregate
// transition, not an approval from an independent reviewer. Preserve the actual
// implementer and never create an approved review or invent review attribution.
func (c *Client) autoParentStatus(ctx context.Context, observed *Record, target models.Status, actor string, verifyGraph func() error) (*Record, error) {
	d, err := observed.CopyDetails()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	supersedeReviews(&d, now)
	d.Status = target
	d.ImplementerSession = observed.ImplementerSession
	d.ReviewHandoffID = ""
	d.ReviewHandoffUpdatedAt = time.Time{}
	d.ReviewBasis = ""
	d.ReviewEvents = ""
	d.ReviewRequestedBySession = ""
	d.ClosedBySession = ""
	checkedEvents := ""
	if target == models.StatusInReview {
		d.ReviewRequestedBySession = actor
		checkedEvents, err = c.stateEvents(ctx, observed.ID)
		if err != nil {
			return nil, err
		}
		d.ReviewEvents = checkedEvents
		d.ReviewBasis = reviewBasis(observed, d)
	} else {
		d.ClosedBySession = actor
		d.Sessions = append(d.Sessions, models.IssueSessionHistory{ID: "hist-" + rand.Text(), IssueID: observed.ID, SessionID: actor, Action: models.ActionSessionClosed, CreatedAt: now})
	}
	reason := "Auto-cascaded to " + string(target) + " (all children complete)"
	action := "close"
	if target == models.StatusInReview {
		action = "review"
	}
	d.Transitions = append(d.Transitions, TransitionRecord{OperationID: "td-op-" + rand.Text(), Action: action, From: observed.Status, To: target, SessionID: actor, Reason: reason, At: now})
	if err := verifyGraph(); err != nil {
		return nil, err
	}
	if checkedEvents != "" {
		current, err := c.stateEvents(ctx, observed.ID)
		if err != nil {
			return nil, err
		}
		if current != checkedEvents {
			return nil, &ConflictError{ID: observed.ID, AfterWrite: true}
		}
	}
	native := nativeStatus(target)
	return c.UpdateObserved(ctx, observed, Changes{Details: &d, Status: &native, Reason: &reason})
}

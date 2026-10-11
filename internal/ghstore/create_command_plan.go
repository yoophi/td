package ghstore

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"

	"github.com/marcus/td/internal/models"
)

// The preparation boundary owns complete create input and actual external
// observations. A future allocated issue is intentionally absent from records
// and watches. The executor must still journal the intent and recheck these
// observations before each write; preparing a plan grants no write authority.
type creationCommandPlan struct {
	create              *issueCreatePlan
	graph               *creationRelationGraph
	records             map[string]Record
	watches             []ActionRelatedObservation
	relationOperationID string
	events              map[string][]nativeStateEvent
}

func (c *Client) prepareRelatedCreation(ctx context.Context, issue *models.Issue, change RelationChanges, operationID string) (*creationCommandPlan, error) {
	if issue == nil || issue.ID != "" || issue.Status != "" && issue.Status != models.StatusOpen {
		return nil, fmt.Errorf("creation command requires an unidentified open task")
	}
	if err := validateIssue(issue); err != nil {
		return nil, err
	}
	// Detach every caller-owned pointer before the first repository read.
	type request struct {
		Issue     models.Issue
		Relations RelationChanges
	}
	data, err := json.Marshal(request{Issue: *issue, Relations: change})
	if err != nil {
		return nil, err
	}
	var owned request
	if err := json.Unmarshal(data, &owned); err != nil {
		return nil, err
	}
	snapshot, err := c.ReadSnapshot(WithFreshSnapshot(ctx), false)
	if err != nil {
		return nil, err
	}
	p := &creationCommandPlan{records: map[string]Record{}, watches: []ActionRelatedObservation{}, relationOperationID: "td-op-" + rand.Text(), events: map[string][]nativeStateEvent{}}
	for _, record := range snapshot.Issues(true) {
		p.records[record.ID] = record
	}
	p.graph, err = planCreationRelations(p.records, owned.Issue.ParentID, owned.Relations)
	if err != nil {
		return nil, err
	}
	owned.Issue.ParentID = p.graph.ParentID
	p.create, err = prepareIssueCreationInput(&owned.Issue, operationID, p.graph.DependsOn)
	if err != nil {
		return nil, err
	}
	// Parent and dependency observations come from the same bulk collection.
	// Avoid one Get per target during preparation; execution still needs fresh
	// conflict checks, including graph membership and native event evidence.
	for _, id := range p.graph.Parents {
		p.create.parents = append(p.create.parents, p.records[id])
	}
	for _, id := range p.graph.Watches {
		record := p.records[id]
		state, err := actionIssueState(&record)
		if err != nil {
			return nil, err
		}
		p.watches = append(p.watches, ActionRelatedObservation{ID: id, State: state})
	}
	// Relationship edits supersede review authority. Capture any required
	// native event evidence before creation, not after the new task exists.
	for _, id := range p.graph.Blocks {
		r := p.records[id]
		state, err := actionIssueState(&r)
		if err != nil {
			return nil, err
		}
		if updateIntentNeedsEvents(state, state) {
			events, err := c.readStateEvents(withReviewCost(ctx), id)
			if err != nil {
				return nil, err
			}
			p.events[id] = events
		}
	}
	return p, nil
}

package ghstore

import (
	"fmt"
	"slices"

	"github.com/marcus/td/internal/models"
)

// workflowUpdateGraph is the semantic membership of a complete mixed update.
// It contains no invented revisions, timestamps, transition results or native
// events. The executor must freeze original observations and full command inputs
// separately, then bind each effect to actual earlier command results.
type workflowUpdateGraph struct {
	Relations    *relationUpdateGraph
	Actions      map[string]string
	BeforeScopes map[string][]string
	AfterScopes  map[string][]string
	Members      []string
}

// planWorkflowUpdateGraph includes both sides of a hierarchy/dependency edit:
// the old graph is needed for conflict detection and inverse validation, while
// the requested graph determines possible review/closure cascades. Review
// eligibility remains an execution-time decision after edits have been saved.
// This planner does not turn approval of old contents into approval of new ones.
func planWorkflowUpdateGraph(records map[string]Record, ids []string, change Changes, relations RelationChanges, options TransitionOptions) (*workflowUpdateGraph, error) {
	if change.Status == nil || change.Details != nil || change.Reason != nil || len(change.journalRelated) != 0 {
		return nil, fmt.Errorf("workflow command requires a status and public field/relationship inputs")
	}
	live := map[string]Record{}
	for id, r := range records {
		if r.DeletedAt == nil && (r.meta.EntityKind == "" || r.meta.EntityKind == "issue") {
			if id != r.ID || !canonicalGroupID(id) {
				return nil, fmt.Errorf("workflow graph contains an inconsistent task identity")
			}
			live[id] = r
		}
	}
	graph, err := planRelationUpdates(live, ids, relations)
	if err != nil {
		return nil, err
	}
	plan := &workflowUpdateGraph{Relations: graph, Actions: map[string]string{}, BeforeScopes: map[string][]string{}, AfterScopes: map[string][]string{}}
	for _, id := range graph.Selected {
		r := live[id]
		action, err := workflowUpdateRequest(r.Status, *change.Status, options)
		if err == nil && groupBasicCommand(action) {
			_, _, err = basicTransitionDecision(&r, action, options)
		}
		if err != nil {
			return nil, fmt.Errorf("workflow update preflight %s (no write attempted): %w", id, err)
		}
		plan.Actions[id] = action
	}
	parent := ""
	if change.ParentID != nil && *change.ParentID != "" {
		n, err := Number(*change.ParentID)
		if err != nil {
			return nil, err
		}
		parent = fmt.Sprintf("gh-%d", n)
		if _, exists := live[parent]; !exists {
			return nil, fmt.Errorf("workflow parent %s missing or deleted", parent)
		}
	}
	// These records are graph-only projections. Never pass them to a writer or
	// persist them as source observations: they have no native identity/clocks.
	projected := map[string]Record{}
	for id, r := range live {
		edges := []string{}
		if r.Details != nil {
			edges = slices.Clone(r.Details.Dependencies)
		}
		if desired, changed := graph.Desired[id]; changed {
			edges = slices.Clone(desired)
		}
		p := r.ParentID
		if change.ParentID != nil && slices.Contains(graph.Selected, id) {
			p = parent
		}
		projected[id] = Record{Issue: models.Issue{ID: id, ParentID: p, Status: r.Status}, Details: &IssueDetails{Dependencies: edges}}
	}
	seen := map[string]bool{}
	add := func(ids []string) {
		for _, id := range ids {
			if !seen[id] {
				plan.Members = append(plan.Members, id)
				seen[id] = true
			}
		}
	}
	add(graph.Selected)
	add(graph.Order)
	for _, id := range graph.Selected {
		for _, stage := range []struct {
			records  map[string]Record
			scopes   map[string][]string
			original bool
		}{{live, plan.BeforeScopes, true}, {projected, plan.AfterScopes, false}} {
			scope, err := workflowUpdateScope(stage.records, id, plan.Actions[id], options)
			if err != nil && stage.original {
				// A valid reparent can repair a broken original hierarchy. Its
				// old live component remains watched, without inventing missing
				// observations or claiming that restoring it is a valid inverse.
				scope, err = workflowOriginalScope(stage.records, id, plan.Actions[id]), nil
			}
			if err != nil {
				return nil, fmt.Errorf("workflow graph preflight %s (no write attempted): %w", id, err)
			}
			stage.scopes[id] = scope
			add(scope)
		}
	}
	for _, id := range graph.Order {
		add(graph.Related[id])
		for _, states := range []map[string]Record{live, projected} {
			add(workflowDependencyScope(states, id))
		}
	}
	for _, id := range graph.Selected {
		add(graph.Blocked[id])
	}
	return plan, nil
}

// Include removed as well as retained live dependency observations. Old broken
// edges may be repaired; they do not authorize inventing a missing task state.
func workflowDependencyScope(records map[string]Record, root string) []string {
	seen := map[string]bool{root: true}
	ids := []string{}
	var walk func(string)
	walk = func(id string) {
		r, found := records[id]
		if !found || r.Details == nil {
			return
		}
		for _, next := range r.Details.Dependencies {
			if _, live := records[next]; !live || seen[next] {
				continue
			}
			seen[next] = true
			ids = append(ids, next)
			walk(next)
		}
	}
	walk(root)
	return ids
}

func workflowOriginalScope(records map[string]Record, root, action string) []string {
	seen := map[string]bool{root: true}
	ancestors := map[string]bool{}
	for id := records[root].ParentID; id != "" && !seen[id]; {
		r, exists := records[id]
		if !exists {
			break
		}
		seen[id], ancestors[id] = true, true
		id = r.ParentID
	}
	if action == "review" || action == "close" || action == "approve" {
		for id, r := range records {
			if ancestors[r.ParentID] {
				seen[id] = true
			}
		}
	}
	if action == "review" {
		descendants := map[string]bool{root: true}
		for changed := true; changed; {
			changed = false
			for id, r := range records {
				if !descendants[id] && descendants[r.ParentID] {
					descendants[id], seen[id], changed = true, true, true
				}
			}
		}
	}
	if action == "close" || action == "approve" {
		for id, r := range records {
			if r.Details == nil {
				continue
			}
			for _, dep := range r.Details.Dependencies {
				if dep == root || ancestors[dep] {
					seen[id] = true
					for _, watched := range workflowDependencyScope(records, id) {
						seen[watched] = true
					}
				}
			}
		}
	}
	ids := []string{}
	for id := range seen {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

func workflowUpdateScope(records map[string]Record, root, action string, options TransitionOptions) ([]string, error) {
	// Sorting avoids Go map iteration changing a command's frozen membership.
	ids := make([]string, 0, len(records))
	for id := range records {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	ordered := make([]Record, 0, len(ids))
	for _, id := range ids {
		ordered = append(ordered, records[id])
	}
	scope := []string{root}
	if action == "review" || nativeGroupCommand(action) {
		var err error
		scope, err = graphGroupIDs(&ActionGroup{Command: action, Roots: []string{root}, Cascade: action == "close" || action == "approve" && !options.RecordOnly}, ordered)
		if err != nil {
			return nil, err
		}
	}
	// Even a basic status edit can also reparent the task. Pin both old/new
	// ancestor chains so a later inverse cannot restore a newly cyclic graph.
	ancestors := map[string]bool{root: true}
	for parent := records[root].ParentID; parent != ""; {
		if ancestors[parent] {
			return nil, fmt.Errorf("parent cycle involving %s", parent)
		}
		ancestors[parent] = true
		r, found := records[parent]
		if !found {
			return nil, fmt.Errorf("parent %s missing or deleted", parent)
		}
		if !slices.Contains(scope, parent) {
			scope = append(scope, parent)
		}
		parent = r.ParentID
	}
	return scope, nil
}

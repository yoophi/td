package ghstore

import (
	"fmt"
	"slices"
)

// This name exists only in a graph used to validate requested edges. It is not
// an issue identity and must never enter a native body, receipt or observation.
const creationGraphNode = "<created-issue>"

type creationRelationGraph struct {
	ParentID  string
	DependsOn []string
	Blocks    []string
	Parents   []string
	Watches   []string
	Original  map[string][]string
}

// planCreationRelations validates the full final graph from one repository
// observation, including paths that cross the as-yet-unallocated issue. Only
// IDs and requested edges are projected; original records remain unchanged.
func planCreationRelations(records map[string]Record, parent string, change RelationChanges) (*creationRelationGraph, error) {
	live, edges := map[string]Record{}, map[string][]string{}
	for id, r := range records {
		if r.DeletedAt != nil || r.meta.EntityKind != "" && r.meta.EntityKind != "issue" {
			continue
		}
		if !canonicalGroupID(id) || r.ID != id {
			return nil, fmt.Errorf("creation graph contains an inconsistent existing identity")
		}
		live[id] = r
		edges[id] = []string{}
		if r.Details != nil {
			edges[id] = slices.Clone(r.Details.Dependencies)
		}
	}
	canonical := func(raw string) (string, error) {
		n, err := Number(raw)
		if err != nil {
			return "", err
		}
		id := fmt.Sprintf("gh-%d", n)
		if _, exists := live[id]; !exists {
			return "", fmt.Errorf("creation relationship target %s missing or deleted", id)
		}
		return id, nil
	}
	resolve := func(values *[]string) ([]string, error) {
		out, seen := []string{}, map[string]bool{}
		if values == nil {
			return out, nil
		}
		for _, raw := range *values {
			id, err := canonical(raw)
			if err != nil {
				return nil, err
			}
			if seen[id] {
				return nil, fmt.Errorf("creation relationship repeats %s", id)
			}
			seen[id] = true
			out = append(out, id)
		}
		return out, nil
	}
	g := &creationRelationGraph{Original: map[string][]string{}, Parents: []string{}, Watches: []string{}}
	var err error
	if g.DependsOn, err = resolve(change.DependsOn); err != nil {
		return nil, err
	}
	if g.Blocks, err = resolve(change.Blocks); err != nil {
		return nil, err
	}
	watched := map[string]bool{}
	if parent != "" {
		if g.ParentID, err = canonical(parent); err != nil {
			return nil, err
		}
		ancestors := map[string]bool{}
		for id := g.ParentID; id != ""; id = live[id].ParentID {
			if ancestors[id] {
				return nil, fmt.Errorf("creation parent chain contains a cycle at %s", id)
			}
			if !canonicalGroupID(id) {
				return nil, fmt.Errorf("creation ancestor %q must use a canonical issue ID", id)
			}
			if _, exists := live[id]; !exists {
				return nil, fmt.Errorf("creation parent %s missing or deleted", id)
			}
			ancestors[id], watched[id] = true, true
			g.Parents = append(g.Parents, id)
		}
	}
	edges[creationGraphNode] = slices.Clone(g.DependsOn)
	for _, id := range g.Blocks {
		g.Original[id] = slices.Clone(edges[id])
		edges[id] = append(slices.Clone(edges[id]), creationGraphNode)
	}
	for _, source := range append([]string{creationGraphNode}, g.Blocks...) {
		targets, err := relationGraphTargets(edges, source)
		if err != nil {
			return nil, fmt.Errorf("creation relationship preflight (no write attempted): %w", err)
		}
		for _, id := range append(targets, source) {
			if id != creationGraphNode {
				watched[id] = true
			}
		}
	}
	for id := range watched {
		g.Watches = append(g.Watches, id)
	}
	slices.Sort(g.Watches)
	return g, nil
}

// Binding is permitted only after the executor verifies the actual creation
// response. This produces requested dependency values, never a before-state or
// a guessed native number. Collision checks use the original repository view.
func (g *creationRelationGraph) bindCreatedID(id string, original map[string]Record) (map[string][]string, error) {
	if !canonicalGroupID(id) {
		return nil, fmt.Errorf("creation result requires a canonical allocated issue ID")
	}
	if _, existed := original[id]; existed {
		return nil, fmt.Errorf("creation result reused an original issue identity")
	}
	updates := map[string][]string{}
	for _, target := range g.Blocks {
		updates[target] = append(slices.Clone(g.Original[target]), id)
	}
	return updates, nil
}

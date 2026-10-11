package ghstore

import (
	"fmt"
	"maps"
	"slices"
)

// RelationChanges retains the distinction between omitted and cleared edges.
type RelationChanges struct {
	DependsOn *[]string
	Blocks    *[]string
}

type relationUpdateGraph struct {
	Selected []string
	Order    []string
	Desired  map[string][]string
	Related  map[string][]string
	Blocked  map[string][]string
}

// Plan the complete replacement over actual observations. Repeated reverse
// targets acquire one final delta, not competing payloads based on old states.
// These maps contain requested edges only, never fabricated native revisions.
func planRelationUpdates(records map[string]Record, ids []string, change RelationChanges) (*relationUpdateGraph, error) {
	canonical := func(values []string) ([]string, error) {
		out, seen := []string{}, map[string]bool{}
		for _, raw := range values {
			n, err := Number(raw)
			if err != nil {
				return nil, err
			}
			id := fmt.Sprintf("gh-%d", n)
			if seen[id] {
				return nil, fmt.Errorf("relationship command repeats %s; no write attempted", id)
			}
			seen[id] = true
			r, found := records[id]
			if !found || r.ID != id || r.DeletedAt != nil {
				return nil, fmt.Errorf("relationship target %s missing or deleted; no write attempted", id)
			}
			out = append(out, id)
		}
		return out, nil
	}
	selected, err := canonical(ids)
	if err != nil {
		return nil, err
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("relationship command requires selected tasks")
	}
	var depends, blocks []string
	if change.DependsOn != nil {
		depends, err = canonical(*change.DependsOn)
		if err != nil {
			return nil, err
		}
		for _, source := range selected {
			if slices.Contains(depends, source) {
				return nil, fmt.Errorf("update relationship preflight %s (no write attempted): circular dependency on the same issue", source)
			}
		}
	}
	if change.Blocks != nil {
		blocks, err = canonical(*change.Blocks)
		if err != nil {
			return nil, err
		}
	}
	original := map[string][]string{}
	for id, r := range records {
		if r.DeletedAt != nil {
			continue
		}
		original[id] = []string{}
		if r.Details != nil {
			original[id] = slices.Clone(r.Details.Dependencies)
		}
	}
	desired := maps.Clone(original)
	g := &relationUpdateGraph{Selected: selected, Desired: map[string][]string{}, Related: map[string][]string{}, Blocked: map[string][]string{}}
	validate := map[string]bool{}
	for _, source := range selected {
		if change.DependsOn != nil {
			desired[source] = slices.Clone(depends)
			validate[source] = true
		}
		if change.Blocks == nil {
			continue
		}
		if slices.Contains(blocks, source) {
			return nil, fmt.Errorf("cannot block the same issue %s", source)
		}
		g.Blocked[source] = []string{}
		for id, edges := range original {
			if slices.Contains(edges, source) {
				g.Blocked[source] = append(g.Blocked[source], id)
			}
		}
		slices.Sort(g.Blocked[source])
		for id, edges := range desired {
			if id == source {
				continue
			}
			wanted, exists := slices.Contains(blocks, id), slices.Contains(edges, source)
			if wanted && !exists {
				desired[id] = append(slices.Clone(edges), source)
			} else if !wanted && exists {
				desired[id] = slices.DeleteFunc(slices.Clone(edges), func(dep string) bool { return dep == source })
			}
		}
	}
	pending := slices.Clone(selected)
	extra := []string{}
	for id, edges := range desired {
		if !slices.Equal(edges, original[id]) {
			validate[id] = true
			if !slices.Contains(selected, id) {
				extra = append(extra, id)
			}
		}
	}
	slices.Sort(extra)
	pending = append(pending, extra...)
	for _, id := range pending {
		g.Desired[id] = slices.Clone(desired[id])
		if validate[id] {
			if _, err := relationGraphTargets(desired, id); err != nil {
				return nil, fmt.Errorf("update relationship preflight %s (no write attempted): %w", id, err)
			}
		}
	}
	current := maps.Clone(original)
	for len(pending) > 0 {
		chosen := -1
		var related []string
		for i, id := range pending {
			if !validate[id] {
				chosen = i
				break
			}
			old := current[id]
			current[id] = desired[id]
			observed, err := relationGraphTargets(current, id)
			current[id] = old
			if err == nil {
				chosen, related = i, observed
				break
			}
		}
		if chosen < 0 {
			return nil, fmt.Errorf("cannot safely order relationship replacements; no write attempted")
		}
		id := pending[chosen]
		g.Order = append(g.Order, id)
		g.Related[id] = related
		current[id] = desired[id]
		pending = slices.Delete(pending, chosen, chosen+1)
	}
	return g, nil
}

// Return every reachable live target in deterministic traversal order.
// Removed dangling edges do not participate in the requested graph.
func relationGraphTargets(graph map[string][]string, source string) ([]string, error) {
	visiting, visited := map[string]bool{source: true}, map[string]bool{}
	result := []string{}
	var walk func(string) error
	walk = func(id string) error {
		if visiting[id] {
			return fmt.Errorf("circular dependency involving %s", id)
		}
		if visited[id] {
			return nil
		}
		edges, found := graph[id]
		if !found {
			return fmt.Errorf("dependency %s missing or deleted", id)
		}
		visiting[id] = true
		result = append(result, id)
		for _, next := range edges {
			if err := walk(next); err != nil {
				return err
			}
		}
		delete(visiting, id)
		visited[id] = true
		return nil
	}
	for _, id := range graph[source] {
		if err := walk(id); err != nil {
			return nil, err
		}
	}
	return result, nil
}

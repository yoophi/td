package ghstore

import (
	"fmt"
	"slices"
)

type importDetachRequiredError struct{}

func (*importDetachRequiredError) Error() string {
	return "import graph transition requires intermediate detachments"
}

// Choose a field-write order against the observed graph, rather than relying on
// input order. Final source DAGs can require removing an old edge first. Parent
// and dependency graphs are independent and both must remain valid after each
// issue write. A transition requiring intermediate detachments remains explicit.
func importGraphOrder(plan *ImportPlan, relations map[string]importRelations) ([]int, error) {
	parents := map[string]string{}
	dependencies := map[string][]string{}
	for _, record := range plan.snapshot.Issues(true) {
		parents[record.ID] = record.ParentID
		if record.Details != nil {
			dependencies[record.ID] = slices.Clone(record.Details.Dependencies)
		}
	}
	pending := []int{}
	for i, row := range plan.Rows {
		if row.Action == "adopt" {
			continue
		}
		want := relations[row.SourceKey]
		if row.Action == "skip" || row.Action == "resume_history" {
			got := slices.Clone(dependencies[row.TargetID])
			slices.Sort(got)
			if parents[row.TargetID] != want.parent || !slices.Equal(got, want.dependencies) {
				return nil, fmt.Errorf("%s skipped/history-only destination relationships differ from mapped source; inspect mappings before import", row.TargetID)
			}
		}
		if row.Action != "skip" {
			pending = append(pending, i)
		}
	}
	order := make([]int, 0, len(pending))
	for len(pending) > 0 {
		progressed := false
		for offset, index := range pending {
			row := plan.Rows[index]
			want := relations[row.SourceKey]
			oldParent, oldDependencies := parents[row.TargetID], dependencies[row.TargetID]
			parents[row.TargetID], dependencies[row.TargetID] = want.parent, want.dependencies
			parentEdges := func(id string) []string {
				if parents[id] == "" {
					return nil
				}
				return []string{parents[id]}
			}
			depEdges := func(id string) []string { return dependencies[id] }
			if importReachableCycle(row.TargetID, parentEdges) || importReachableCycle(row.TargetID, depEdges) {
				parents[row.TargetID], dependencies[row.TargetID] = oldParent, oldDependencies
				continue
			}
			order = append(order, index)
			pending = append(pending[:offset], pending[offset+1:]...)
			progressed = true
			break
		}
		if !progressed {
			return nil, &importDetachRequiredError{}
		}
	}
	return order, nil
}

func importReachableCycle(root string, edges func(string) []string) bool {
	visiting, visited := map[string]bool{}, map[string]bool{}
	var walk func(string) bool
	walk = func(id string) bool {
		if visiting[id] {
			return true
		}
		if visited[id] {
			return false
		}
		visiting[id] = true
		for _, next := range edges(id) {
			if walk(next) {
				return true
			}
		}
		delete(visiting, id)
		visited[id] = true
		return false
	}
	return walk(root)
}

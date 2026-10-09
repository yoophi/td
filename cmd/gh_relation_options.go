package cmd

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/marcus/td/internal/ghstore"
	"github.com/spf13/cobra"
)

type gitHubRelationOptions struct{ depends, blocks *[]string }

func (o gitHubRelationOptions) changed() bool { return o.depends != nil || o.blocks != nil }
func readGitHubRelationOptions(cmd *cobra.Command) (gitHubRelationOptions, error) {
	options := gitHubRelationOptions{}
	for _, name := range []string{"depends-on", "blocks"} {
		if !cmd.Flags().Changed(name) {
			continue
		}
		values, err := cmd.Flags().GetStringArray(name)
		if err != nil {
			return options, err
		}
		ids := []string{}
		for _, value := range mergeMultiValueFlag(values) {
			if strings.TrimSpace(value) == "" {
				continue
			}
			id, err := canonicalGitHubID(value)
			if err != nil {
				return options, err
			}
			if !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
		if name == "depends-on" {
			options.depends = &ids
		} else {
			options.blocks = &ids
		}
	}
	return options, nil
}

type gitHubRelationRow struct {
	observed ghstore.Record
	desired  []string
}
type gitHubRelationPlan struct {
	source         *ghstore.Record
	rows           map[string]gitHubRelationRow
	graph          map[string][]string
	blocksDesired  *[]string
	blocksObserved []string
}

// Plan the final graph before writing any issue fields. Native GitHub has no
// transaction across sources, so apply only observed rows and report partials.
func prepareGitHubRelations(ctx context.Context, client *ghstore.Client, source *ghstore.Record, options gitHubRelationOptions) (*gitHubRelationPlan, error) {
	plan := &gitHubRelationPlan{source: source, rows: map[string]gitHubRelationRow{}, graph: map[string][]string{}}
	records := map[string]ghstore.Record{source.ID: *source}
	if options.blocks != nil {
		rows, err := client.List(ctx, true)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			records[row.ID] = row
		}
		records[source.ID] = *source
		// Targets must be read directly, including when the listing is lagged.
		for _, id := range *options.blocks {
			if id == source.ID {
				return nil, fmt.Errorf("cannot block the same issue")
			}
			r, err := client.Get(ctx, id)
			if err != nil {
				return nil, err
			}
			records[id] = *r
		}
	}
	if options.blocks != nil {
		plan.blocksDesired = options.blocks
		for id, row := range records {
			if row.Details != nil && slices.Contains(row.Details.Dependencies, source.ID) {
				plan.blocksObserved = append(plan.blocksObserved, id)
			}
		}
		sort.Strings(plan.blocksObserved)
	}
	deps := func(r ghstore.Record) []string {
		if r.Details == nil {
			return []string{}
		}
		return append([]string{}, r.Details.Dependencies...)
	}
	for id, row := range records {
		plan.graph[id] = deps(row)
	}
	if options.depends != nil {
		plan.rows[source.ID] = gitHubRelationRow{*source, append([]string{}, (*options.depends)...)}
	}
	if options.blocks != nil {
		for id, row := range records {
			if id == source.ID {
				continue
			}
			old := deps(row)
			desired := append([]string{}, old...)
			wanted := slices.Contains(*options.blocks, id)
			existing := slices.Contains(old, source.ID)
			if wanted && !existing {
				desired = append(desired, source.ID)
			} else if !wanted && existing {
				desired = slices.DeleteFunc(desired, func(dep string) bool { return dep == source.ID })
			}
			if !slices.Equal(old, desired) {
				plan.rows[id] = gitHubRelationRow{row, desired}
			}
		}
	}
	final := map[string][]string{}
	for id, edges := range plan.graph {
		final[id] = edges
	}
	for id, row := range plan.rows {
		final[id] = row.desired
	}
	// Resolve only reachable final edges. Removed missing/deleted targets can be
	// cleaned; a dangling desired target must never count as resolved.
	visiting, visited := map[string]bool{}, map[string]bool{}
	var walk func(string) error
	walk = func(id string) error {
		if visiting[id] {
			return fmt.Errorf("relationship replacement would create circular dependency involving %s", id)
		}
		if visited[id] {
			return nil
		}
		if _, ok := final[id]; !ok {
			r, err := client.Get(ctx, id)
			if err != nil {
				return fmt.Errorf("read relationship target %s: %w", id, err)
			}
			final[id] = deps(*r)
			plan.graph[id] = final[id]
		}
		visiting[id] = true
		for _, target := range final[id] {
			if err := walk(target); err != nil {
				return err
			}
		}
		delete(visiting, id)
		visited[id] = true
		return nil
	}
	for id := range plan.rows {
		if err := walk(id); err != nil {
			return nil, err
		}
	}
	return plan, nil
}

func relationGraphAcyclic(graph map[string][]string, root string) bool {
	visiting, done := map[string]bool{}, map[string]bool{}
	var walk func(string) bool
	walk = func(id string) bool {
		if visiting[id] {
			return false
		}
		if done[id] {
			return true
		}
		visiting[id] = true
		for _, target := range graph[id] {
			if !walk(target) {
				return false
			}
		}
		delete(visiting, id)
		done[id] = true
		return true
	}
	return walk(root)
}

func applyGitHubRelations(ctx context.Context, client *ghstore.Client, plan *gitHubRelationPlan, source *ghstore.Record, actor string) (*ghstore.Record, error) {
	if row, ok := plan.rows[source.ID]; ok {
		row.observed = *source
		plan.rows[source.ID] = row
	}
	if plan.blocksDesired != nil {
		if err := verifyGitHubBlockedMembership(ctx, client, source.ID, plan.blocksObserved); err != nil {
			return nil, fmt.Errorf("blocked membership changed before relationship writes; earlier field changes may remain: %w", err)
		}
	}
	completed := []string{}
	pending := []string{}
	for id := range plan.rows {
		pending = append(pending, id)
	}
	sort.Strings(pending)
	result := source
	for len(pending) > 0 {
		chosen := -1
		for i, id := range pending {
			old := plan.graph[id]
			plan.graph[id] = plan.rows[id].desired
			valid := relationGraphAcyclic(plan.graph, id)
			plan.graph[id] = old
			if valid {
				chosen = i
				break
			}
		}
		if chosen < 0 {
			return nil, fmt.Errorf("cannot safely order relationship changes; completed sources=%v; earlier changes remain", completed)
		}
		id := pending[chosen]
		row := plan.rows[id]
		updated, noop, err := client.ReplaceDependenciesObserved(ctx, &row.observed, row.desired, actor)
		if err != nil {
			return nil, fmt.Errorf("relationship replacement failed for %s; completed sources=%v; earlier field/status/relation changes remain, inspect current state before retrying: %w", id, completed, err)
		}
		if id == source.ID {
			result = updated
		}
		if !noop {
			completed = append(completed, id)
		}
		plan.graph[id] = row.desired
		pending = slices.Delete(pending, chosen, chosen+1)
	}
	if plan.blocksDesired != nil {
		if err := verifyGitHubBlockedMembership(ctx, client, source.ID, *plan.blocksDesired); err != nil {
			return nil, fmt.Errorf("blocked membership verification failed after writes; completed sources=%v; earlier changes remain: %w", completed, err)
		}
	}
	return result, nil
}

// Membership is a paginated best-effort observation, not a distributed lock.
func verifyGitHubBlockedMembership(ctx context.Context, client *ghstore.Client, root string, expected []string) error {
	rows, err := client.List(ctx, true)
	if err != nil {
		return err
	}
	actual := []string{}
	for _, row := range rows {
		if row.Details != nil && slices.Contains(row.Details.Dependencies, root) {
			actual = append(actual, row.ID)
		}
	}
	sorted := append([]string{}, expected...)
	sort.Strings(sorted)
	sort.Strings(actual)
	if !slices.Equal(sorted, actual) {
		return fmt.Errorf("blocked sources for %s changed: expected %v, observed %v; retry reads before further changes", root, sorted, actual)
	}
	return nil
}

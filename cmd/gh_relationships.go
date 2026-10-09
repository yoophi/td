package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/ghcontext"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/output"
	"github.com/spf13/cobra"
)

func init() {
	for _, original := range []*cobra.Command{depCmd, depAddCmd, depRmCmd, dependsOnCmd, blockedByCmd, criticalPathCmd, treeCmd} {
		local := original.RunE
		original.RunE = func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(getBaseDir())
			if err != nil {
				return err
			}
			kind, err := config.Store(cfg)
			if err != nil {
				return err
			}
			if kind == config.StoreSQLite {
				return local(cmd, args)
			}
			cmd.SilenceUsage = true
			cmd.SetOut(cmd.OutOrStdout())
			client, err := ghstore.Open(cmd.Context(), getBaseDir(), cfg.GitHub)
			if err != nil {
				return err
			}
			if original == depAddCmd || original == depRmCmd || (original == depCmd && len(args) == 2) {
				return changeGitHubDependencies(original, cmd, args, cfg, client)
			}
			return queryGitHubRelationships(original, cmd, args, client)
		}
	}
}

func canonicalGitHubID(id string) (string, error) {
	n, err := ghstore.Number(id)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("gh-%d", n), nil
}

func changeGitHubDependencies(original, cmd *cobra.Command, args []string, cfg *models.Config, client *ghstore.Client) error {
	if len(args) == 0 {
		return fmt.Errorf("issue ID required")
	}
	id, err := canonicalGitHubID(args[0])
	if err != nil {
		return err
	}
	targets := append([]string{}, args[1:]...)
	if original == depAddCmd {
		flag, _ := cmd.Flags().GetString("depends-on")
		for _, value := range strings.Split(flag, ",") {
			if value = strings.TrimSpace(value); value != "" {
				targets = append(targets, value)
			}
		}
	}
	if len(targets) == 0 {
		return fmt.Errorf("no dependencies specified")
	}
	// Reject malformed/cross-repository IDs before any remote write.
	for i, target := range targets {
		targets[i], err = canonicalGitHubID(target)
		if err != nil {
			return err
		}
	}
	dir, err := gitHubContextDirectory()
	if err != nil {
		return err
	}
	scope, err := ghcontext.Resolve(cmd.Context(), dir, cfg.GitHub.Repo)
	if err != nil {
		return err
	}
	state, err := scope.Update(cmd.Context(), nil)
	if err != nil {
		return err
	}
	added := original != depRmCmd
	completed := []string{}
	for _, target := range targets {
		observed, err := client.Get(cmd.Context(), id)
		if err != nil {
			return dependencyBatchError(id, target, completed, err)
		}
		exists := observed.Details != nil && slices.Contains(observed.Details.Dependencies, target)
		var result *ghstore.Record
		if added && exists {
			// A duplicate is a no-op, but still validate that the target is an issue.
			if _, err = client.Get(cmd.Context(), target); err == nil {
				result = observed
			}
		} else {
			result, err = client.ChangeDependencyObserved(cmd.Context(), observed, target, added, state.Session.ID)
		}
		if err != nil {
			return dependencyBatchError(id, target, completed, err)
		}
		completed = append(completed, target)
		action := "dependency_added"
		if !added {
			action = "dependency_removed"
		}
		if jsonMode(cmd) {
			if err = json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"action": action, "from": id, "to": target, "type": "depends_on", "already_exists": added && exists, "issue": result}); err != nil {
				return err
			}
		} else {
			cmd.Printf("%s: %s -> %s\n", strings.ToUpper(action), id, target)
		}
	}
	return nil
}

func dependencyBatchError(id, target string, completed []string, err error) error {
	if len(completed) == 0 {
		return fmt.Errorf("dependency %s -> %s: %w", id, target, err)
	}
	return fmt.Errorf("dependency %s -> %s failed; completed targets %s; earlier changes remain, inspect current state before retrying: %w", id, target, strings.Join(completed, ", "), err)
}

type githubRelationshipGraph struct {
	records map[string]ghstore.Record
	ids     []string
}

func loadGitHubRelationshipGraph(ctx context.Context, client *ghstore.Client, root string) (*githubRelationshipGraph, error) {
	rows, err := client.List(ctx, true)
	if err != nil {
		return nil, err
	}
	g := &githubRelationshipGraph{records: map[string]ghstore.Record{}}
	for _, r := range rows {
		if _, ok := g.records[r.ID]; ok {
			return nil, fmt.Errorf("GitHub listing repeated %s; retry the read", r.ID)
		}
		g.records[r.ID] = r
	}
	if root != "" {
		r, err := client.Get(ctx, root)
		if err != nil {
			return nil, err
		}
		g.records[r.ID] = *r
	}
	// A lagged listing must not make unresolved dependencies appear resolved.
	// Direct reads also reject missing/deleted targets and PRs explicitly.
	queue := make([]string, 0, len(g.records))
	for id := range g.records {
		queue = append(queue, id)
	}
	for i := 0; i < len(queue); i++ {
		r := g.records[queue[i]]
		refs := g.dependencies(r.ID)
		if r.ParentID != "" {
			refs = append(append([]string{}, refs...), r.ParentID)
		}
		for _, ref := range refs {
			if _, ok := g.records[ref]; !ok {
				row, err := client.Get(ctx, ref)
				if err != nil {
					return nil, fmt.Errorf("relationship %s -> %s: %w", r.ID, ref, err)
				}
				g.records[ref] = *row
				queue = append(queue, ref)
			}
		}
	}
	for id := range g.records {
		g.ids = append(g.ids, id)
	}
	sort.Strings(g.ids)
	for _, parent := range []bool{false, true} {
		visiting, visited := map[string]bool{}, map[string]bool{}
		var walk func(string) error
		walk = func(id string) error {
			if visiting[id] {
				return fmt.Errorf("relationship cycle involving %s (parent=%t)", id, parent)
			}
			if visited[id] {
				return nil
			}
			visiting[id] = true
			refs := g.dependencies(id)
			if parent {
				refs = nil
				if g.records[id].ParentID != "" {
					refs = []string{g.records[id].ParentID}
				}
			}
			for _, next := range refs {
				if err := walk(next); err != nil {
					return err
				}
			}
			delete(visiting, id)
			visited[id] = true
			return nil
		}
		for _, id := range g.ids {
			if err := walk(id); err != nil {
				return nil, err
			}
		}
	}
	return g, nil
}

func (g *githubRelationshipGraph) dependencies(id string) []string {
	r := g.records[id]
	if r.Details == nil {
		return []string{}
	}
	return append([]string{}, r.Details.Dependencies...)
}
func (g *githubRelationshipGraph) reverse(id string) []string {
	result := []string{}
	for _, other := range g.ids {
		if slices.Contains(g.dependencies(other), id) {
			result = append(result, other)
		}
	}
	return result
}
func (g *githubRelationshipGraph) transitive(id string, openOnly bool) []string {
	seen := map[string]bool{id: true}
	result := []string{}
	var walk func(string)
	walk = func(current string) {
		for _, next := range g.reverse(current) {
			if seen[next] {
				continue
			}
			seen[next] = true
			if openOnly && g.records[next].Status == models.StatusClosed {
				continue
			}
			result = append(result, next)
			walk(next)
		}
	}
	walk(id)
	return result
}

func queryGitHubRelationships(original, cmd *cobra.Command, args []string, client *ghstore.Client) error {
	root := ""
	var err error
	if original != criticalPathCmd {
		if len(args) != 1 {
			return fmt.Errorf("requires one issue ID")
		}
		root, err = canonicalGitHubID(args[0])
		if err != nil {
			return err
		}
	}
	depth, _ := cmd.Flags().GetInt("depth")
	if depth < 0 {
		return fmt.Errorf("--depth must not be negative")
	}
	limit, _ := cmd.Flags().GetInt("limit")
	if limit < 0 {
		return fmt.Errorf("--limit must not be negative")
	}
	g, err := loadGitHubRelationshipGraph(cmd.Context(), client, root)
	if err != nil {
		return err
	}
	if original == treeCmd {
		return emitGitHubTree(cmd, g, root, depth)
	}
	if original == criticalPathCmd {
		return emitGitHubCriticalPath(cmd, g, limit)
	}
	record := g.records[root]
	blocking, _ := cmd.Flags().GetBool("blocking")
	reverse := original == blockedByCmd || (original == depCmd && blocking)
	ids := g.dependencies(root)
	key := "dependencies"
	if reverse {
		ids = g.reverse(root)
		key = "blocked"
	}
	result := map[string]any{"issue": record, key: ids}
	if original == blockedByCmd {
		direct, _ := cmd.Flags().GetBool("direct")
		result = map[string]any{"issue": record, "direct": ids, "direct_count": len(ids)}
		if !direct {
			all := g.transitive(root, false)
			result["all"] = all
			result["transitive_count"] = len(all) - len(ids)
		}
	}
	if jsonMode(cmd) {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}
	cmd.Println(output.IssueOneLiner(&record.Issue))
	if original == blockedByCmd {
		direct, _ := cmd.Flags().GetBool("direct")
		if !direct {
			ids = g.transitive(root, false)
		}
	}
	for _, id := range ids {
		row := g.records[id]
		cmd.Println(output.DependencyLine(&row.Issue, true))
	}
	if len(ids) == 0 {
		cmd.Println("No matching relationships")
	}
	return nil
}

func emitGitHubTree(cmd *cobra.Command, g *githubRelationshipGraph, root string, maxDepth int) error {
	var build func(string, int, map[string]bool) (map[string]any, error)
	build = func(id string, depth int, visiting map[string]bool) (map[string]any, error) {
		if visiting[id] {
			return nil, fmt.Errorf("parent cycle involving %s", id)
		}
		visiting[id] = true
		defer delete(visiting, id)
		r := g.records[id]
		children := []map[string]any{}
		if maxDepth == 0 || depth < maxDepth {
			for _, child := range g.ids {
				if g.records[child].ParentID == id {
					node, err := build(child, depth+1, visiting)
					if err != nil {
						return nil, err
					}
					children = append(children, node)
				}
			}
		}
		return map[string]any{"id": id, "title": r.Title, "type": r.Type, "status": r.Status, "priority": r.Priority, "children": children}, nil
	}
	tree, err := build(root, 0, map[string]bool{})
	if err != nil {
		return err
	}
	if jsonMode(cmd) {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(tree)
	}
	var printNode func(map[string]any, int)
	printNode = func(node map[string]any, depth int) {
		cmd.Printf("%s%s [%s] %s\n", strings.Repeat("  ", depth), node["id"], node["status"], output.SanitizeIssueText(fmt.Sprint(node["title"])))
		for _, child := range node["children"].([]map[string]any) {
			printNode(child, depth+1)
		}
	}
	printNode(tree, 0)
	return nil
}

func emitGitHubCriticalPath(cmd *cobra.Command, g *githubRelationshipGraph, limit int) error {
	if limit == 0 {
		limit = 10
	}
	candidates := map[string]bool{}
	counts := map[string]int{}
	for _, id := range g.ids {
		r := g.records[id]
		if r.Type != models.TypeEpic && (r.Status == models.StatusOpen || r.Status == models.StatusInProgress || r.Status == models.StatusBlocked) {
			candidates[id] = true
			counts[id] = len(g.transitive(id, true))
		}
	}
	less := func(a, b string) bool {
		if counts[a] != counts[b] {
			return counts[a] > counts[b]
		}
		if g.records[a].Priority != g.records[b].Priority {
			return g.records[a].Priority < g.records[b].Priority
		}
		return a < b
	}
	ready := []string{}
	remaining := map[string]int{}
	dependents := map[string][]string{}
	for id := range candidates {
		remaining[id] = 0
		for _, dep := range g.dependencies(id) {
			if g.records[dep].Status != models.StatusClosed {
				remaining[id]++
				if candidates[dep] {
					dependents[dep] = append(dependents[dep], id)
				}
			}
		}
		if remaining[id] == 0 {
			ready = append(ready, id)
		}
	}
	sort.Slice(ready, func(i, j int) bool { return less(ready[i], ready[j]) })
	start := []string{}
	for _, id := range ready {
		if counts[id] > 0 {
			start = append(start, id)
		}
	}
	sequence := []string{}
	for len(ready) > 0 {
		sort.Slice(ready, func(i, j int) bool { return less(ready[i], ready[j]) })
		id := ready[0]
		ready = ready[1:]
		sequence = append(sequence, id)
		for _, dependent := range dependents[id] {
			remaining[dependent]--
			if remaining[dependent] == 0 {
				ready = append(ready, dependent)
			}
		}
	}
	type score struct {
		ID    string `json:"id"`
		Score int    `json:"score"`
	}
	ranking := []score{}
	for id, count := range counts {
		if count > 0 {
			ranking = append(ranking, score{id, count})
		}
	}
	sort.Slice(ranking, func(i, j int) bool { return less(ranking[i].ID, ranking[j].ID) })
	if jsonMode(cmd) {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"critical_path": sequence, "ready_to_start": start, "bottleneck_ranking": ranking})
	}
	cmd.Println("CRITICAL PATH SEQUENCE:")
	for i, id := range sequence {
		if i >= limit {
			break
		}
		cmd.Printf("%d. %s: %s (unblocks %d)\n", i+1, id, output.SanitizeIssueText(g.records[id].Title), counts[id])
	}
	if len(start) > 0 {
		cmd.Println("START NOW:")
		for i, id := range start {
			if i >= 3 {
				break
			}
			cmd.Printf("%s: %s (unblocks %d)\n", id, output.SanitizeIssueText(g.records[id].Title), counts[id])
		}
	}
	if len(ranking) > 0 {
		cmd.Println("BOTTLENECKS:")
		for i, row := range ranking {
			if i >= 3 {
				break
			}
			cmd.Printf("%s: %d issues waiting\n", row.ID, row.Score)
		}
	}
	if len(sequence) == 0 {
		cmd.Println("No ready sequence; unresolved dependencies may be waiting for review or contain a cycle")
	}
	return nil
}

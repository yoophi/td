package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/marcus/td/internal/ghcontext"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/output"
	"github.com/spf13/cobra"
)

var errMultipleGitHubRoots = errors.New("multiple issue roots found")

type githubHierarchy struct {
	ctx     context.Context
	client  *ghstore.Client
	records map[string]ghstore.Record
}

func newGitHubHierarchy(ctx context.Context, client *ghstore.Client, rows []ghstore.Record) *githubHierarchy {
	h := &githubHierarchy{ctx: ctx, client: client, records: map[string]ghstore.Record{}}
	for _, r := range rows {
		h.records[r.ID] = r
	}
	return h
}
func (h *githubHierarchy) get(id string) (ghstore.Record, error) {
	if row, ok := h.records[id]; ok {
		return row, nil
	}
	r, err := h.client.Get(h.ctx, id)
	if err != nil {
		return ghstore.Record{}, err
	}
	h.records[r.ID] = *r
	return *r, nil
}
func (h *githubHierarchy) root(id string) (string, error) {
	seen := map[string]bool{}
	for {
		if seen[id] {
			return "", fmt.Errorf("parent cycle involving %s", id)
		}
		seen[id] = true
		r, err := h.get(id)
		if err != nil {
			return "", err
		}
		if r.ParentID == "" {
			return r.ID, nil
		}
		id = r.ParentID
	}
}
func (h *githubHierarchy) commonRoot(ids []string) (string, error) {
	root := ""
	for _, id := range ids {
		candidate, err := h.root(id)
		if err != nil {
			return "", err
		}
		if root != "" && root != candidate {
			return "", errMultipleGitHubRoots
		}
		root = candidate
	}
	return root, nil
}
func (h *githubHierarchy) descendant(id, ancestor string) (bool, error) {
	seen := map[string]bool{id: true}
	row, err := h.get(id)
	if err != nil {
		return false, err
	}
	found := false
	for row.ParentID != "" {
		id = row.ParentID
		if seen[id] {
			return false, fmt.Errorf("parent cycle involving %s", id)
		}
		seen[id] = true
		if id == ancestor {
			found = true
		}
		row, err = h.get(id)
		if err != nil {
			return false, err
		}
	}
	return found, nil
}

func resolveGitHubHierarchyFilter(cmd *cobra.Command, raw, name string, cfg *models.Config, h *githubHierarchy, rows []ghstore.Record) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if raw != "." {
		id, err := canonicalGitHubID(raw)
		if err != nil {
			return "", err
		}
		r, err := h.client.Get(cmd.Context(), id)
		if err != nil {
			return "", err
		}
		h.records[id] = *r
		if _, err := h.root(id); err != nil {
			return "", err
		}
		return id, nil
	}
	dir, err := gitHubContextDirectory()
	if err != nil {
		return "", err
	}
	scope, err := ghcontext.Resolve(cmd.Context(), dir, cfg.GitHub.Repo)
	if err != nil {
		return "", err
	}
	state, err := scope.Update(cmd.Context(), nil)
	if err != nil {
		return "", err
	}
	if state.Focus != "" {
		r, err := h.client.Get(cmd.Context(), state.Focus)
		if err != nil {
			return "", fmt.Errorf("resolve --%s . saved focus %s: %w", name, state.Focus, err)
		}
		h.records[r.ID] = *r
		return r.ID, nil
	}
	ids := []string{}
	for _, r := range rows {
		if r.ImplementerSession == state.Session.ID && (r.Status == models.StatusInProgress || r.Status == models.StatusInReview) {
			ids = append(ids, r.ID)
		}
	}
	var ambiguity error
	if root, err := h.commonRoot(ids); err == nil && root != "" {
		return root, nil
	} else if err != nil {
		if !errors.Is(err, errMultipleGitHubRoots) {
			return "", err
		}
		ambiguity = err
	}
	// SQLite's fallback uses session logs. On GitHub those are shared activities
	// and transition records, not all issues merely created by the same actor.
	ids = nil
	for _, r := range rows {
		touched := false
		if r.Details != nil {
			for _, event := range r.Details.Transitions {
				if event.SessionID == state.Session.ID {
					touched = true
				}
			}
		}
		activities, err := h.client.ListActivity(cmd.Context(), r.ID)
		if err != nil {
			return "", fmt.Errorf("resolve --%s . activity for %s: %w", name, r.ID, err)
		}
		for _, a := range activities {
			if a.SessionID == state.Session.ID && (a.Kind == "log" || a.Kind == "handoff") {
				touched = true
			}
		}
		if touched {
			ids = append(ids, r.ID)
		}
	}
	if root, err := h.commonRoot(ids); err == nil && root != "" {
		return root, nil
	} else if err != nil {
		if !errors.Is(err, errMultipleGitHubRoots) {
			return "", err
		}
		ambiguity = err
	}
	if state.ActiveWorkSession != "" {
		ws, err := state.CurrentWorkSession()
		if err != nil {
			return "", err
		}
		if root, err := h.commonRoot(ws.Issues); err == nil && root != "" {
			return root, nil
		} else if err != nil {
			if !errors.Is(err, errMultipleGitHubRoots) {
				return "", err
			}
			ambiguity = err
		}
	}
	if ambiguity != nil {
		return "", fmt.Errorf("resolve --%s .: %w", name, ambiguity)
	}
	return "", fmt.Errorf("--%s . requires a focused issue, recent session activity, or an active work session", name)
}

type gitHubIssueWithChildren struct {
	gitHubShowDetail
	Children []ghstore.Record `json:"children"`
}

func showGitHubHierarchy(cmd *cobra.Command, args []string, client *ghstore.Client, format string) error {
	cmd.SetOut(cmd.OutOrStdout())
	tree, _ := cmd.Flags().GetBool("tree")
	children, _ := cmd.Flags().GetBool("children")
	if tree {
		if len(args) != 1 {
			return fmt.Errorf("show --tree requires one issue ID")
		}
		id, err := canonicalGitHubID(args[0])
		if err != nil {
			return err
		}
		g, err := loadGitHubRelationshipGraph(cmd.Context(), client, id)
		if err != nil {
			return err
		}
		if format == "json" {
			if cmd.Flags().Lookup("json") != nil {
				_ = cmd.Flags().Set("json", "true")
			}
		}
		return emitGitHubTree(cmd, g, id, 0)
	}
	records := []ghstore.Record{}
	needChildren := children
	for _, id := range args {
		r, err := client.Get(cmd.Context(), id)
		if err != nil {
			return err
		}
		records = append(records, *r)
		if r.Type == models.TypeEpic {
			needChildren = true
		}
		if warning := r.StateLabelWarning(); warning != "" && !jsonMode(cmd) && format != "json" {
			cmd.PrintErrln("Warning:", warning)
		}
	}
	short, _ := cmd.Flags().GetBool("short")
	detailed := jsonMode(cmd) || format == "json" || (!short && format != "short")
	details := map[string]gitHubShowDetail{}
	if detailed {
		var err error
		details, err = loadGitHubShowDetails(cmd, client, records)
		if err != nil {
			return err
		}
	}
	byParent := map[string][]ghstore.Record{}
	if needChildren {
		rows, err := client.List(cmd.Context(), true)
		if err != nil {
			return err
		}
		h := newGitHubHierarchy(cmd.Context(), client, rows)
		for _, r := range records {
			h.records[r.ID] = r
			if _, err := h.root(r.ID); err != nil {
				return err
			}
		}
		for _, r := range rows {
			if r.ParentID != "" {
				if _, err := h.root(r.ID); err != nil {
					return err
				}
				byParent[r.ParentID] = append(byParent[r.ParentID], r)
			}
		}
		for parent := range byParent {
			sort.Slice(byParent[parent], func(i, j int) bool { return byParent[parent][i].Number < byParent[parent][j].Number })
		}
	}
	if jsonMode(cmd) || format == "json" {
		if children {
			enriched := []gitHubIssueWithChildren{}
			for _, r := range records {
				enriched = append(enriched, gitHubIssueWithChildren{details[r.ID], append([]ghstore.Record{}, byParent[r.ID]...)})
			}
			if len(enriched) == 1 {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(enriched[0])
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(enriched)
		}
		if len(records) == 1 {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(details[records[0].ID])
		}
		enriched := []gitHubShowDetail{}
		for _, r := range records {
			enriched = append(enriched, details[r.ID])
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(enriched)
	}
	for _, r := range records {
		if short || format == "short" {
			cmd.Println(output.FormatIssueShort(&r.Issue))
		} else {
			issue := output.SanitizedForDisplay(&r.Issue)
			if render, _ := cmd.Flags().GetBool("render-markdown"); render {
				issue = renderIssueMarkdown(issue, output.TerminalWidth(80))
			}
			detail := details[r.ID]
			cmd.Print(output.FormatIssueLong(issue, detail.modelLogs, detail.modelHandoff))
			printGitHubShowDetails(cmd, detail)
			cmd.Println(r.URL)
		}
		if children || (!short && format != "short" && r.Type == models.TypeEpic) {
			cmd.Println("CHILDREN:")
			for _, child := range byParent[r.ID] {
				cmd.Println(output.FormatIssueShort(&child.Issue))
			}
			if len(byParent[r.ID]) == 0 {
				cmd.Println("No child issues")
			}
		}
	}
	return nil
}

// Used to apply parent and epic filters together without changing scope.
func filterGitHubHierarchy(h *githubHierarchy, rows []ghstore.Record, parent, epic string, all bool) ([]ghstore.Record, error) {
	filtered := []ghstore.Record{}
	for _, row := range rows {
		if !all && row.Status == models.StatusClosed {
			continue
		}
		if parent != "" && row.ParentID != parent {
			continue
		}
		if epic != "" {
			matches, err := h.descendant(row.ID, epic)
			if err != nil {
				return nil, err
			}
			if !matches {
				continue
			}
		}
		filtered = append(filtered, row)
	}
	return filtered, nil
}

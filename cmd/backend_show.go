package cmd

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/output"
	"github.com/spf13/cobra"
)

func openBackendCommand(cmd *cobra.Command) (*backendstore.Connection, error) {
	dir, e := gitHubContextDirectory()
	if e != nil {
		return nil, e
	}
	return backendstore.Open(cmd.Context(), getBaseDir(), dir)
}

func resolveBackendID(rows []backendstore.Issue, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("backend issue ID required")
	}
	if !strings.HasPrefix(raw, "td-") {
		raw = "td-" + raw
	}
	matches := []string{}
	for _, row := range rows {
		if row.ID == raw {
			return row.ID, nil
		}
		if strings.HasPrefix(row.ID, raw) {
			matches = append(matches, row.ID)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("ambiguous backend issue prefix %q: %s", raw, strings.Join(matches, ", "))
	}
	return "", fmt.Errorf("backend issue %q not found", raw)
}

type backendShowDetail struct {
	backendstore.Issue
	Logs         []models.Log             `json:"logs"`
	Handoff      *models.Handoff          `json:"handoff,omitempty"`
	Files        []models.IssueFile       `json:"files"`
	Dependencies []models.IssueDependency `json:"dependencies"`
	BlockedBy    []models.IssueDependency `json:"blocked_by"`
	Comments     []backendstore.Comment   `json:"comments"`
	Children     []backendstore.Issue     `json:"children"`
}

func runBackendShow(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	cmd.SetOut(cmd.OutOrStdout())
	format, _ := cmd.Flags().GetString("format")
	if !slices.Contains([]string{"", "json", "short", "long"}, format) {
		return fmt.Errorf("unsupported output format %q", format)
	}
	tree, _ := cmd.Flags().GetBool("tree")
	children, _ := cmd.Flags().GetBool("children")
	short, _ := cmd.Flags().GetBool("short")
	if tree && len(args) > 1 {
		return fmt.Errorf("show --tree requires one issue ID")
	}
	connection, e := openBackendCommand(cmd)
	if e != nil {
		return e
	}
	var rows []backendstore.Issue
	loadRows := func() error {
		if rows != nil {
			return nil
		}
		var e error
		rows, e = connection.List(cmd.Context(), true)
		return e
	}
	if len(args) == 0 {
		focus, e := connection.Focus(cmd.Context())
		if e != nil {
			return e
		}
		if focus != "" {
			args = []string{focus}
		} else {
			if e = loadRows(); e != nil {
				return e
			}
			for _, status := range []models.Status{models.StatusInProgress, models.StatusInReview} {
				candidates := []string{}
				for _, row := range rows {
					if row.Status == status {
						candidates = append(candidates, row.ID)
					}
				}
				if len(candidates) == 1 {
					args = candidates
					break
				}
				if len(candidates) > 1 {
					return fmt.Errorf("no backend issue ID specified: multiple issues %s", status)
				}
			}
			if len(args) == 0 {
				return fmt.Errorf("no backend issue ID specified and no active issue")
			}
		}
	}
	ids := make([]string, 0, len(args))
	for _, raw := range args {
		id := strings.TrimSpace(raw)
		if len(id) != 19 || !strings.HasPrefix(id, "td-") {
			if e = loadRows(); e != nil {
				return e
			}
			id, e = resolveBackendID(rows, raw)
			if e != nil {
				return e
			}
		}
		ids = append(ids, id)
	}
	if tree {
		if e = loadRows(); e != nil {
			return e
		}
		return showBackendTree(cmd, rows, ids[0], jsonMode(cmd) || format == "json")
	}
	details := make([]backendShowDetail, 0, len(ids))
	for _, id := range ids {
		detail, e := connection.Detail(cmd.Context(), id)
		if e != nil {
			return e
		}
		details = append(details, backendShowDetail{detail.Issue, jsonList(detail.Logs), detail.LatestHandoff, jsonList(detail.Files), jsonList(detail.Dependencies), jsonList(detail.BlockedBy), jsonList(detail.Comments), jsonList(detail.Children)})
	}
	if jsonMode(cmd) || format == "json" {
		if len(details) == 1 {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(details[0])
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(details)
	}
	lines := []string{}
	for _, detail := range details {
		var text strings.Builder
		if short || format == "short" {
			text.WriteString(output.FormatIssueShort(&detail.Issue.Issue) + "\n")
		} else {
			issue := output.SanitizedForDisplay(&detail.Issue.Issue)
			if render, _ := cmd.Flags().GetBool("render-markdown"); render {
				issue = renderIssueMarkdown(issue, output.TerminalWidth(80))
			}
			text.WriteString(output.FormatIssueLong(issue, detail.Logs, detail.Handoff))
			if len(detail.Reviews) > 0 && string(detail.Reviews) != "null" {
				var reviews []models.IssueReview
				if e := json.Unmarshal(detail.Reviews, &reviews); e != nil {
					return fmt.Errorf("invalid backend review history")
				}
				if len(reviews) > 0 {
					text.WriteString("RECENT REVIEWS:\n")
				}
				for _, review := range reviews[max(0, len(reviews)-3):] {
					marker := ""
					if review.SupersededAt != nil {
						marker = " [superseded]"
					}
					if review.ReviewedBy != "" {
						marker += " (reviewed by " + output.SanitizeIssueText(review.ReviewedBy) + ")"
					} else if review.SelfReview {
						marker += " (self-review)"
					}
					text.WriteString(fmt.Sprintf("  %s by %s: %s%s\n", output.SanitizeIssueText(review.Decision), output.SanitizeIssueText(review.ReviewerSession), output.IndentContinuation(review.Summary), marker))
				}
			}
			if len(detail.Comments) > 0 {
				text.WriteString("COMMENTS:\n")
				for _, comment := range detail.Comments {
					text.WriteString(fmt.Sprintf("  [%s] %s: %s\n", comment.CreatedAt.Format("2006-01-02 15:04"), output.SanitizeIssueText(comment.SessionID), output.IndentContinuation(comment.Text)))
				}
			}
			if len(detail.Files) > 0 {
				text.WriteString("FILES:\n")
				for _, file := range detail.Files {
					text.WriteString(fmt.Sprintf("  %s (%s)\n", output.SanitizeIssueText(file.FilePath), file.Role))
				}
			}
			if len(detail.Dependencies) > 0 {
				text.WriteString("DEPENDS ON:\n")
				for _, dep := range detail.Dependencies {
					text.WriteString("  " + output.SanitizeIssueText(dep.DependsOnID) + "\n")
				}
			}
			if len(detail.BlockedBy) > 0 {
				text.WriteString("BLOCKS:\n")
				for _, dep := range detail.BlockedBy {
					text.WriteString("  " + output.SanitizeIssueText(dep.IssueID) + "\n")
				}
			}
		}
		if children || (!short && format != "short" && detail.Type == models.TypeEpic) {
			text.WriteString("CHILDREN:\n")
			for _, child := range detail.Children {
				text.WriteString("  " + output.FormatIssueShort(&child.Issue) + "\n")
			}
			if len(detail.Children) == 0 {
				text.WriteString("No child issues\n")
			}
		}
		lines = append(lines, text.String())
	}
	for _, line := range lines {
		cmd.Print(line)
	}
	return nil
}

func showBackendTree(cmd *cobra.Command, rows []backendstore.Issue, id string, isJSON bool) error {
	type node struct {
		backendstore.Issue
		Children []node `json:"children"`
	}
	byID := map[string]backendstore.Issue{}
	byParent := map[string][]backendstore.Issue{}
	for _, row := range rows {
		byID[row.ID] = row
		if row.ParentID != "" {
			byParent[row.ParentID] = append(byParent[row.ParentID], row)
		}
	}
	if _, ok := byID[id]; !ok {
		return fmt.Errorf("backend tree root %s not found", id)
	}
	seen := map[string]bool{}
	var build func(string) (node, output.TreeNode, error)
	build = func(id string) (node, output.TreeNode, error) {
		if seen[id] {
			return node{}, output.TreeNode{}, fmt.Errorf("cycle in backend issue tree")
		}
		seen[id] = true
		defer delete(seen, id)
		issue := byID[id]
		out := node{Issue: issue, Children: []node{}}
		rendered := output.TreeNode{ID: issue.ID, Title: output.SanitizeIssueText(issue.Title), Status: issue.Status, Type: issue.Type}
		for _, child := range byParent[id] {
			n, r, e := build(child.ID)
			if e != nil {
				return node{}, output.TreeNode{}, e
			}
			out.Children = append(out.Children, n)
			rendered.Children = append(rendered.Children, r)
		}
		return out, rendered, nil
	}
	out, rendered, e := build(id)
	if e != nil {
		return e
	}
	if isJSON {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(out)
	}
	cmd.Printf("%s %s: %s\n", out.Type, out.ID, output.SanitizeIssueText(out.Title))
	if tree := output.RenderTree(output.TreeNode{Children: rendered.Children}, output.TreeRenderOptions{ShowStatus: true, ShowType: true}); tree != "" {
		cmd.Println(tree)
	}
	return nil
}

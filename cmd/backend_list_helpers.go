package cmd

import (
	"encoding/json"
	"fmt"
	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/internal/issuestore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/output"
	"github.com/marcus/td/internal/query"
	"github.com/spf13/cobra"
	"slices"
	"strings"
)

func backendListLines(snapshot *issuestore.BackendQuerySnapshot, issues []models.Issue, long bool) ([]string, error) {
	lines := []string{}
	for _, issue := range issues {
		if long {
			logs, e := snapshot.GetLogs(issue.ID, 5)
			if e != nil {
				return nil, e
			}
			handoff, e := snapshot.GetLatestHandoff(issue.ID)
			if e != nil {
				return nil, e
			}
			lines = append(lines, output.FormatIssueLong(output.SanitizedForDisplay(&issue), logs, handoff)+"---\n")
		} else {
			lines = append(lines, output.FormatIssueShort(&issue)+"\n")
		}
	}
	return lines, nil
}
func listBackendTDQ(cmd *cobra.Command, expression string) error {
	if e := validateGitHubQuery(expression); e != nil {
		return e
	}
	limit, _ := cmd.Flags().GetInt("limit")
	if limit < 0 {
		return fmt.Errorf("limit must be zero (unlimited) or positive")
	}
	sortBy, _ := cmd.Flags().GetString("sort")
	if e := validateGitHubQuerySort(expression, sortBy); e != nil {
		return e
	}
	format, _ := cmd.Flags().GetString("format")
	if !slices.Contains([]string{"", "short", "long", "json"}, format) {
		return fmt.Errorf("unsupported output format %q", format)
	}
	c, e := openBackendCommand(cmd)
	if e != nil {
		return e
	}
	var result query.ExecuteResult
	var lines []string
	e = c.ReadSnapshot(cmd.Context(), func(rows []backendstore.Issue) error {
		snapshot, e := issuestore.NewBackendQuerySnapshot(cmd.Context(), c, rows)
		if e != nil {
			return e
		}
		reverse, _ := cmd.Flags().GetBool("reverse")
		result, e = query.ExecuteDetailed(snapshot, expression, c.SessionID, query.ExecuteOptions{Limit: limit, SortBy: sortBy, SortDesc: reverse})
		if e != nil {
			return e
		}
		if jsonMode(cmd) || format == "json" {
			return nil
		}
		long, _ := cmd.Flags().GetBool("long")
		lines, e = backendListLines(snapshot, result.Issues, long || format == "long")
		return e
	})
	if e != nil {
		return e
	}
	if result.Truncated {
		cmd.PrintErrf("Warning: showing %d of %d matches (--limit %d; use -n 0 for all)\n", len(result.Issues), result.Matched, limit)
	}
	if result.ScanLimited {
		cmd.PrintErrf("Warning: only the first %d issues were scanned; use td query --max-scan to raise this limit\n", query.DefaultMaxResults)
	}
	if jsonMode(cmd) || format == "json" {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(jsonList(result.Issues))
	}
	for _, line := range lines {
		cmd.Print(line)
	}
	if len(result.Issues) == 0 {
		cmd.Println("No issues found")
	}
	return nil
}
func resolveBackendListHierarchy(cmd *cobra.Command, c *backendstore.Connection, snapshot *issuestore.BackendQuerySnapshot, rows []backendstore.Issue, raw, name string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if raw != "." {
		return resolveBackendID(rows, raw)
	}
	focus, e := c.Focus(cmd.Context())
	if e != nil {
		return "", e
	}
	if focus != "" {
		return resolveBackendID(rows, focus)
	}
	byID := map[string]backendstore.Issue{}
	for _, row := range rows {
		byID[row.ID] = row
	}
	rootOf := func(id string) (string, error) {
		if _, ok := byID[id]; !ok {
			return "", fmt.Errorf("backend hierarchy issue is absent")
		}
		seen := map[string]bool{}
		for byID[id].ParentID != "" {
			if seen[id] {
				return "", fmt.Errorf("cycle in backend hierarchy")
			}
			seen[id] = true
			id = byID[id].ParentID
			if _, ok := byID[id]; !ok {
				return "", fmt.Errorf("backend hierarchy parent is absent")
			}
		}
		return id, nil
	}
	common := func(ids []string) (string, error) {
		root := ""
		for _, id := range ids {
			r, e := rootOf(id)
			if e != nil {
				return "", e
			}
			if root != "" && root != r {
				return "", fmt.Errorf("--%s . spans multiple issue roots", name)
			}
			root = r
		}
		return root, nil
	}
	active := []string{}
	for _, row := range rows {
		if row.ImplementerSession == c.SessionID && (row.Status == models.StatusInProgress || row.Status == models.StatusInReview) {
			active = append(active, row.ID)
		}
	}
	var ambiguity error
	if root, e := common(active); e == nil && root != "" {
		return root, nil
	} else if e != nil {
		ambiguity = e
	}
	touched := []string{}
	for _, row := range rows {
		logs, e := snapshot.GetLogs(row.ID, 0)
		if e != nil {
			return "", e
		}
		match := false
		for _, log := range logs {
			if log.SessionID == c.SessionID {
				match = true
			}
		}
		h, e := snapshot.GetLatestHandoff(row.ID)
		if e != nil {
			return "", e
		}
		if h != nil && h.SessionID == c.SessionID {
			match = true
		}
		if match {
			touched = append(touched, row.ID)
		}
	}
	if root, e := common(touched); e != nil {
		ambiguity = e
	} else if root != "" {
		return root, nil
	}
	ids, e := c.ActiveWorkIssueIDs(cmd.Context())
	if e != nil {
		return "", e
	}
	if root, e := common(ids); e != nil {
		ambiguity = e
	} else if root != "" {
		return root, nil
	}
	if ambiguity != nil {
		return "", ambiguity
	}
	return "", fmt.Errorf("--%s . requires a focused issue, recent session activity, or an active work session", name)
}

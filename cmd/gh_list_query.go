package cmd

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/marcus/td/internal/issuestore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/output"
	"github.com/marcus/td/internal/query"
	"github.com/spf13/cobra"
)

func listGitHubTDQ(cmd *cobra.Command, expression string, cfg *models.Config) error {
	if err := validateGitHubQuery(expression); err != nil {
		return err
	}
	limit, _ := cmd.Flags().GetInt("limit")
	if limit < 0 {
		return fmt.Errorf("limit must be zero (unlimited) or positive")
	}
	sortBy, _ := cmd.Flags().GetString("sort")
	if err := validateGitHubQuerySort(expression, sortBy); err != nil {
		return err
	}
	format, _ := cmd.Flags().GetString("format")
	if !slices.Contains([]string{"", "short", "long", "json"}, format) {
		return fmt.Errorf("unsupported output format %q", format)
	}
	snapshot, state, err := openGitHubQuerySnapshot(cmd, cfg)
	if err != nil {
		return err
	}
	reverse, _ := cmd.Flags().GetBool("reverse")
	result, err := query.ExecuteDetailed(snapshot, expression, state.Session.ID, query.ExecuteOptions{Limit: limit, SortBy: sortBy, SortDesc: reverse})
	if err != nil {
		return err
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
	long, _ := cmd.Flags().GetBool("long")
	return printGitHubListIssues(cmd, snapshot, result.Issues, long || format == "long")
}

func printGitHubListIssues(cmd *cobra.Command, snapshot *issuestore.GitHubQuerySnapshot, issues []models.Issue, long bool) error {
	// Assemble detailed output before printing: failed activity reads must not
	// leave a partial listing looking like a complete successful result.
	lines := make([]string, 0, len(issues))
	for _, issue := range issues {
		if long {
			logs, err := snapshot.GetLogs(issue.ID, 5)
			if err != nil {
				return err
			}
			handoff, err := snapshot.GetLatestHandoff(issue.ID)
			if err != nil {
				return err
			}
			lines = append(lines, output.FormatIssueLong(output.SanitizedForDisplay(&issue), logs, handoff)+"---\n")
		} else {
			lines = append(lines, output.FormatIssueShort(&issue)+"\n")
		}
	}
	for _, line := range lines {
		cmd.Print(line)
	}
	if len(issues) == 0 {
		cmd.Println("No issues found")
	}
	return nil
}

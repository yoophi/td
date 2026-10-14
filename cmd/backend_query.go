package cmd

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/internal/issuestore"
	"github.com/marcus/td/internal/output"
	"github.com/marcus/td/internal/query"
	"github.com/spf13/cobra"
)

func runBackendQuery(cmd *cobra.Command, expression string) error {
	if e := validateGitHubQuery(expression); e != nil {
		return e
	}
	format, _ := cmd.Flags().GetString("output")
	if !slices.Contains([]string{"table", "json", "ids", "count"}, format) {
		return fmt.Errorf("unsupported query output %q", format)
	}
	limit, _ := cmd.Flags().GetInt("limit")
	maxScan, _ := cmd.Flags().GetInt("max-scan")
	if limit < 0 || maxScan < 0 {
		return fmt.Errorf("limit and max-scan must be zero or positive")
	}
	if maxScan == 0 {
		maxScan = query.DefaultMaxResults
	}
	sortBy, _ := cmd.Flags().GetString("sort")
	desc := strings.HasPrefix(sortBy, "-")
	sortBy = strings.TrimPrefix(sortBy, "-")
	if e := validateGitHubQuerySort(expression, sortBy); e != nil {
		return e
	}
	connection, e := openBackendCommand(cmd)
	if e != nil {
		return e
	}
	var result query.ExecuteResult
	e = connection.ReadSnapshot(cmd.Context(), func(rows []backendstore.Issue) error {
		snapshot, e := issuestore.NewBackendQuerySnapshot(cmd.Context(), connection, rows)
		if e != nil {
			return e
		}
		result, e = query.ExecuteDetailed(snapshot, expression, connection.SessionID, query.ExecuteOptions{Limit: limit, SortBy: sortBy, SortDesc: desc, MaxResults: maxScan})
		return e
	})
	if e != nil {
		return e
	}
	cmd.SetOut(cmd.OutOrStdout())
	if result.Truncated {
		cmd.PrintErrf("Warning: showing %d of %d matches (--limit %d; use -n 0 for all)\n", len(result.Issues), result.Matched, limit)
	}
	if result.ScanLimited {
		cmd.PrintErrf("Warning: only the first %d issues were scanned; anything beyond that was not considered (raise with --max-scan)\n", maxScan)
	}
	if jsonMode(cmd) {
		format = "json"
	}
	switch format {
	case "json":
		return json.NewEncoder(cmd.OutOrStdout()).Encode(jsonList(result.Issues))
	case "count":
		cmd.Println(result.Matched)
	case "ids":
		for _, issue := range result.Issues {
			cmd.Println(issue.ID)
		}
	default:
		for _, issue := range result.Issues {
			cmd.Println(output.FormatIssueShort(&issue))
		}
		if len(result.Issues) == 0 {
			cmd.Println("No issues matching query")
		}
	}
	return nil
}

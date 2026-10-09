package cmd

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/ghcontext"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/issuestore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/output"
	"github.com/marcus/td/internal/query"
	"github.com/spf13/cobra"
)

func init() {
	localRun := queryCmd.RunE
	queryCmd.RunE = func(cmd *cobra.Command, args []string) error {
		// Help, examples, field inventory and parsing are deliberately offline.
		for _, flag := range []string{"examples", "fields", "explain"} {
			if enabled, _ := cmd.Flags().GetBool(flag); enabled {
				return localRun(cmd, args)
			}
		}
		if len(args) == 0 {
			return cmd.Help()
		}
		cfg, err := config.Load(getBaseDir())
		if err != nil {
			return err
		}
		store, err := config.Store(cfg)
		if err != nil {
			return err
		}
		if store == config.StoreSQLite {
			return localRun(cmd, args)
		}
		cmd.SilenceUsage = true
		cmd.SetOut(cmd.OutOrStdout())
		return runGitHubQuery(cmd, args[0], cfg)
	}
}

func openGitHubQuerySnapshot(cmd *cobra.Command, cfg *models.Config) (*issuestore.GitHubQuerySnapshot, *ghcontext.State, error) {
	client, state, rows, err := openGitHubQueryData(cmd, cfg)
	if err != nil {
		return nil, nil, err
	}
	snapshot, err := issuestore.NewGitHubQuerySnapshot(cmd.Context(), rows, client)
	return snapshot, state, err
}

func openGitHubQueryData(cmd *cobra.Command, cfg *models.Config) (*ghstore.Client, *ghcontext.State, []ghstore.Record, error) {
	client, err := ghstore.Open(cmd.Context(), getBaseDir(), cfg.GitHub)
	if err != nil {
		return nil, nil, nil, err
	}
	state, err := resolveGitHubListState(cmd, cfg)
	if err != nil {
		return nil, nil, nil, err
	}
	rows, err := client.List(cmd.Context(), true)
	if err != nil {
		return nil, nil, nil, err
	}
	return client, state, rows, nil
}

func validateGitHubQuery(expression string) error {
	parsed, err := query.Parse(expression)
	if err != nil {
		return fmt.Errorf("parse error: %w", err)
	}
	if errs := parsed.Validate(); len(errs) > 0 {
		return fmt.Errorf("validation error: %w", errs[0])
	}
	return nil
}

func validateGitHubQuerySort(expression, sortBy string) error {
	parsed, err := query.Parse(expression)
	if err != nil {
		return err
	}
	if parsed.Sort != nil {
		sortBy = parsed.Sort.Field
	}
	return issuestore.SortGitHubIssues(nil, sortBy, false)
}

func runGitHubQuery(cmd *cobra.Command, expression string, cfg *models.Config) error {
	if err := validateGitHubQuery(expression); err != nil {
		return err
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
	sortDesc := strings.HasPrefix(sortBy, "-")
	sortBy = strings.TrimPrefix(sortBy, "-")
	if err := validateGitHubQuerySort(expression, sortBy); err != nil {
		return err
	}
	snapshot, state, err := openGitHubQuerySnapshot(cmd, cfg)
	if err != nil {
		return err
	}
	result, err := query.ExecuteDetailed(snapshot, expression, state.Session.ID, query.ExecuteOptions{Limit: limit, SortBy: sortBy, SortDesc: sortDesc, MaxResults: maxScan})
	if err != nil {
		return err
	}
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

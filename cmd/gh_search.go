package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/db"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/output"
	"github.com/spf13/cobra"
)

func init() {
	localRun := searchCmd.RunE
	searchCmd.RunE = func(cmd *cobra.Command, args []string) error {
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
		return runGitHubSearch(cmd, args[0], cfg)
	}
}

func runGitHubSearch(cmd *cobra.Command, text string, cfg *models.Config) error {
	opts := db.ListIssuesOptions{}
	statuses, _ := cmd.Flags().GetStringArray("status")
	for _, value := range mergeMultiValueFlag(statuses) {
		status := models.NormalizeStatus(value)
		if !models.IsValidStatus(status) {
			return fmt.Errorf("invalid status %q", value)
		}
		opts.Status = append(opts.Status, status)
	}
	types, _ := cmd.Flags().GetStringArray("type")
	for _, value := range mergeMultiValueFlag(types) {
		typ := models.NormalizeType(value)
		if !models.IsValidType(typ) {
			return fmt.Errorf("invalid type %q", value)
		}
		opts.Type = append(opts.Type, typ)
	}
	labels, _ := cmd.Flags().GetStringArray("labels")
	opts.Labels = mergeMultiValueFlag(labels)
	priority, _ := cmd.Flags().GetString("priority")
	if priority != "" {
		p := models.NormalizePriority(priority)
		if !models.IsValidPriority(p) {
			return fmt.Errorf("invalid priority %q", priority)
		}
		opts.Priority = string(p)
	}
	opts.Limit, _ = cmd.Flags().GetInt("limit")
	if opts.Limit < 0 {
		return fmt.Errorf("search limit must be zero or positive")
	}
	if opts.Limit == 0 {
		opts.Limit = 50
	}
	snapshot, _, err := openGitHubQuerySnapshot(cmd, cfg)
	if err != nil {
		return err
	}
	results, err := snapshot.SearchIssuesRanked(text, opts)
	if err != nil {
		return err
	}
	if jsonMode(cmd) {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(results)
	}
	showScore, _ := cmd.Flags().GetBool("show-score")
	for _, r := range results {
		line := output.FormatIssueShort(&r.Issue)
		if showScore {
			line += fmt.Sprintf(" [score:%d %s]", r.Score, r.MatchField)
		}
		cmd.Println(line)
	}
	if len(results) == 0 {
		cmd.Printf("No issues matching %q\n", output.SanitizeIssueText(text))
		cmd.Println("Searched: id, title, description, logs, handoffs. Comments are not searched (try: td query comment.text).")
	}
	return nil
}

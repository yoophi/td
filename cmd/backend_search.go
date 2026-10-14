package cmd

import (
	"encoding/json"
	"fmt"
	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/internal/db"
	"github.com/marcus/td/internal/issuestore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/output"
	"github.com/spf13/cobra"
)

func runBackendSearch(cmd *cobra.Command, text string) error {
	cmd.SilenceUsage = true
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
	connection, err := openBackendCommand(cmd)
	if err != nil {
		return err
	}
	var results []db.SearchResult
	err = connection.ReadSnapshot(cmd.Context(), func(rows []backendstore.Issue) error {
		snapshot, e := issuestore.NewBackendQuerySnapshot(cmd.Context(), connection, rows)
		if e != nil {
			return e
		}
		results, e = snapshot.SearchIssuesRanked(text, opts)
		return e
	})
	if err != nil {
		return err
	}
	cmd.SetOut(cmd.OutOrStdout())
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

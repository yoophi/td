package cmd

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/issuestore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/output"
	"github.com/spf13/cobra"
)

func listGitHubIssues(cmd *cobra.Command, args []string, _ *models.Config) error {
	if len(args) > 0 {
		return fmt.Errorf("gh-issue does not support positional TDQ queries; use --search or supported filter flags")
	}
	all, _ := cmd.Flags().GetBool("all")
	open, _ := cmd.Flags().GetBool("open")
	statuses, _ := cmd.Flags().GetStringArray("status")
	statuses = mergeMultiValueFlag(statuses)
	for _, status := range statuses {
		if status != "open" && status != "closed" && status != "all" {
			return fmt.Errorf("gh-issue status must be open, closed or all")
		}
		if status == "closed" || status == "all" {
			all = true
		}
	}
	if open {
		all = false
		statuses = []string{"open"}
	}
	types, _ := cmd.Flags().GetStringArray("type")
	types = mergeMultiValueFlag(types)
	for i, value := range types {
		typ := models.NormalizeType(value)
		if !models.IsValidType(typ) {
			return fmt.Errorf("invalid type %q", value)
		}
		types[i] = string(typ)
	}
	priority, _ := cmd.Flags().GetString("priority")
	if priority != "" {
		priority = string(models.NormalizePriority(priority))
		if !models.IsValidPriority(models.Priority(priority)) {
			return fmt.Errorf("invalid priority %q", priority)
		}
	}
	ids, _ := cmd.Flags().GetStringArray("id")
	ids = mergeMultiValueFlag(ids)
	for i, id := range ids {
		n, err := ghstore.Number(id)
		if err != nil {
			return err
		}
		ids[i] = fmt.Sprintf("gh-%d", n)
	}
	labels, _ := cmd.Flags().GetStringArray("labels")
	labels = mergeMultiValueFlag(labels)
	search, _ := cmd.Flags().GetString("search")
	search = strings.ToLower(search)
	sortBy, _ := cmd.Flags().GetString("sort")
	if sortBy == "" {
		sortBy = "priority"
	}
	if !slices.Contains([]string{"id", "title", "status", "type", "priority", "points", "created_at", "updated_at"}, sortBy) {
		return fmt.Errorf("unsupported gh-issue sort field %q", sortBy)
	}
	limit, _ := cmd.Flags().GetInt("limit")
	if limit < 0 {
		return fmt.Errorf("limit must be zero (unlimited) or positive")
	}
	format, _ := cmd.Flags().GetString("format")
	if !slices.Contains([]string{"", "short", "long", "json"}, format) {
		return fmt.Errorf("unsupported output format %q", format)
	}
	client, err := issuestore.OpenReader(cmd.Context(), getBaseDir())
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	records, err := client.List(cmd.Context(), all)
	if err != nil {
		return err
	}
	filtered := make([]issuestore.Record, 0)
	for _, record := range records {
		if len(statuses) > 0 && !slices.Contains(statuses, "all") && !slices.Contains(statuses, string(record.Status)) {
			continue
		}
		if len(types) > 0 && !slices.Contains(types, string(record.Type)) {
			continue
		}
		if priority != "" && string(record.Priority) != priority {
			continue
		}
		if len(ids) > 0 && !slices.Contains(ids, record.ID) {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(record.Title+"\n"+record.Description), search) {
			continue
		}
		matches := true
		for _, label := range labels {
			if !slices.Contains(record.Labels, label) {
				matches = false
				break
			}
		}
		if matches {
			filtered = append(filtered, record)
		}
	}
	reverse, _ := cmd.Flags().GetBool("reverse")
	slices.SortFunc(filtered, func(a, b issuestore.Record) int {
		order := 0
		switch sortBy {
		case "id":
			order = cmp.Compare(a.Number, b.Number)
		case "title":
			order = strings.Compare(a.Title, b.Title)
		case "status":
			order = cmp.Compare(a.Status, b.Status)
		case "type":
			order = cmp.Compare(a.Type, b.Type)
		case "priority":
			order = cmp.Compare(a.Priority, b.Priority)
		case "points":
			order = cmp.Compare(a.Points, b.Points)
		case "created_at":
			order = a.CreatedAt.Compare(b.CreatedAt)
		case "updated_at":
			order = a.UpdatedAt.Compare(b.UpdatedAt)
		}
		if order == 0 {
			order = cmp.Compare(a.Number, b.Number)
		}
		if reverse {
			return -order
		}
		return order
	})
	if limit > 0 && len(filtered) > limit {
		filtered = filtered[:limit]
	}
	if jsonMode(cmd) || format == "json" {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(filtered)
	}
	long, _ := cmd.Flags().GetBool("long")
	for _, record := range filtered {
		if long || format == "long" {
			cmd.Print(output.FormatIssueLong(output.SanitizedForDisplay(&record.Issue), nil, nil))
		} else {
			cmd.Println(output.FormatIssueShort(&record.Issue))
		}
	}
	if len(filtered) == 0 {
		cmd.Println("No issues found")
	}
	return nil
}

package cmd

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/issuestore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/output"
	"github.com/spf13/cobra"
)

func listGitHubIssues(cmd *cobra.Command, args []string, cfg *models.Config) error {
	cmd.SetOut(cmd.OutOrStdout())
	expression, _ := cmd.Flags().GetString("filter")
	positional := strings.TrimSpace(strings.Join(args, " "))
	if cmd.Flags().Changed("filter") && expression == "" {
		return fmt.Errorf("--filter requires a non-empty query expression")
	}
	if expression != "" && positional != "" {
		return fmt.Errorf("cannot use both --filter and positional query")
	}
	if positional != "" {
		expression = positional
	}
	if expression != "" {
		return listGitHubTDQ(cmd, expression, cfg)
	}
	extra, err := readGitHubListFilters(cmd)
	if err != nil {
		return err
	}
	reviewable, _ := cmd.Flags().GetBool("reviewable")
	includeApproved, _ := cmd.Flags().GetBool("include-approved")
	all, _ := cmd.Flags().GetBool("all")
	open, _ := cmd.Flags().GetBool("open")
	statuses, _ := cmd.Flags().GetStringArray("status")
	statuses = mergeMultiValueFlag(statuses)
	for i, status := range statuses {
		if strings.EqualFold(status, "all") {
			statuses[i] = "all"
			continue
		}
		normalized := models.NormalizeStatus(status)
		if !models.IsValidStatus(normalized) {
			return fmt.Errorf("gh-issue status must be open, in_progress, blocked, in_review, closed or all")
		}
		statuses[i] = string(normalized)
	}
	includeDeferred := all || slices.Contains(statuses, "all")
	for _, status := range statuses {
		if status == "closed" || status == "all" {
			all = true
		}
	}
	statuses = slices.DeleteFunc(statuses, func(s string) bool { return s == "all" })
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
	searchMatch := issuestore.SQLiteLIKE("%" + search + "%")
	sortBy, _ := cmd.Flags().GetString("sort")
	if sortBy == "" {
		sortBy = "priority"
	}
	if !slices.Contains([]string{"id", "title", "status", "type", "priority", "points", "created_at", "updated_at", "closed_at", "deleted_at", "due_date", "defer_until", "defer_count"}, sortBy) {
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
	parentRaw, _ := cmd.Flags().GetString("parent")
	epicRaw, _ := cmd.Flags().GetString("epic")
	for _, raw := range []string{parentRaw, epicRaw} {
		if raw != "" && strings.TrimSpace(raw) != "." {
			if _, err := canonicalGitHubID(raw); err != nil {
				return err
			}
		}
	}
	client, err := ghstore.Open(cmd.Context(), getBaseDir(), cfg.GitHub)
	if err != nil {
		return err
	}
	actor := ""
	if extra.mine || reviewable {
		state, err := resolveGitHubListState(cmd, cfg)
		if err != nil {
			return err
		}
		actor = state.Session.ID
		if extra.mine {
			extra.implementer = actor
		}
	}
	hierarchyRequested := strings.TrimSpace(parentRaw) != "" || strings.TrimSpace(epicRaw) != ""
	rows, err := client.List(cmd.Context(), all || hierarchyRequested)
	if err != nil {
		return err
	}
	if hierarchyRequested {
		h := newGitHubHierarchy(cmd.Context(), client, rows)
		parent, err := resolveGitHubHierarchyFilter(cmd, parentRaw, "parent", cfg, h, rows)
		if err != nil {
			return err
		}
		epic, err := resolveGitHubHierarchyFilter(cmd, epicRaw, "epic", cfg, h, rows)
		if err != nil {
			return err
		}
		rows, err = filterGitHubHierarchy(h, rows, parent, epic, all)
		if err != nil {
			return err
		}
	}
	awaiting, ready := map[string]bool{}, map[string]bool{}
	records := make([]issuestore.Record, 0, len(rows))
	for _, r := range rows {
		records = append(records, issuestore.Record{Issue: r.Issue, Number: r.Number, URL: r.URL, StateLabelDiagnostic: r.StateLabelDiagnostic})
	}
	filtered := make([]issuestore.Record, 0)
	today := time.Now()
	schedule := gitHubScheduleFilterFromFlags(cmd, includeDeferred)
	for _, record := range records {
		if !extra.matches(record.Issue) {
			continue
		}
		if !all && record.Status == models.StatusClosed {
			continue
		}
		if !schedule.matches(record.Issue, today) {
			continue
		}
		if len(statuses) > 0 && !slices.Contains(statuses, string(record.Status)) {
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
		if search != "" && !searchMatch(record.ID) && !searchMatch(record.Title) && !searchMatch(record.Description) {
			continue
		}
		matches := true
		for _, label := range labels {
			if !gitHubListLabelMatches(record.Labels, label) {
				matches = false
				break
			}
		}
		if matches {
			filtered = append(filtered, record)
		}
	}
	if reviewable {
		candidates := map[string]bool{}
		for _, r := range filtered {
			candidates[r.ID] = true
		}
		reviewRows := []ghstore.Record{}
		for _, r := range rows {
			if candidates[r.ID] {
				reviewRows = append(reviewRows, r)
			}
		}
		awaiting, ready, err = gitHubListReviewBuckets(cmd, client, reviewRows, actor)
		if err != nil {
			return err
		}
		filtered = slices.DeleteFunc(filtered, func(r issuestore.Record) bool { return !awaiting[r.ID] && (!includeApproved || !ready[r.ID]) })
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
		case "closed_at":
			order = compareGitHubOptionalTime(a.ClosedAt, b.ClosedAt)
		case "deleted_at":
			order = compareGitHubOptionalTime(a.DeletedAt, b.DeletedAt)
		case "due_date":
			order = cmp.Compare(optionalString(a.DueDate), optionalString(b.DueDate))
		case "defer_until":
			order = cmp.Compare(optionalString(a.DeferUntil), optionalString(b.DeferUntil))
		case "defer_count":
			order = cmp.Compare(a.DeferCount, b.DeferCount)
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
	for _, record := range filtered {
		if record.StateLabelDiagnostic != "" && !jsonMode(cmd) && format != "json" {
			cmd.PrintErrln("Warning:", record.StateLabelDiagnostic)
		}
	}
	if jsonMode(cmd) || format == "json" {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(filtered)
	}
	long, _ := cmd.Flags().GetBool("long")
	if long || format == "long" {
		snapshot, err := issuestore.NewGitHubQuerySnapshot(cmd.Context(), rows, client)
		if err != nil {
			return err
		}
		issues := []models.Issue{}
		for _, r := range filtered {
			issues = append(issues, r.Issue)
		}
		return printGitHubListIssues(cmd, snapshot, issues, true)
	}
	if reviewable {
		return printGitHubReviewList(cmd, filtered, awaiting, ready, includeApproved)
	}
	for _, record := range filtered {
		cmd.Println(output.FormatIssueShort(&record.Issue))
	}
	if len(filtered) == 0 {
		cmd.Println("No issues found")
	}
	return nil
}

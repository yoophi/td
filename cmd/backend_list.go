package cmd

import (
	"encoding/json"
	"fmt"
	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/internal/issuestore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/output"
	"github.com/spf13/cobra"
	"slices"
	"strings"
	"time"
)

func listBackendIssues(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	cmd.SetOut(cmd.OutOrStdout())
	expression, err := listQueryInput(cmd, args, fixedListType(cmd))
	if err != nil {
		return err
	}
	if expression != "" {
		return listBackendTDQ(cmd, expression)
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
			return fmt.Errorf("backend status must be open, in_progress, blocked, in_review, closed or all")
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
	labels, _ := cmd.Flags().GetStringArray("labels")
	labels = mergeMultiValueFlag(labels)
	search, _ := cmd.Flags().GetString("search")
	searchMatch := issuestore.SQLiteLIKE("%" + search + "%")
	sortBy, _ := cmd.Flags().GetString("sort")
	if sortBy == "" {
		sortBy = "priority"
	}
	if !slices.Contains([]string{"id", "title", "status", "type", "priority", "points", "created_at", "updated_at", "closed_at", "deleted_at", "due_date", "defer_until", "defer_count"}, sortBy) {
		return fmt.Errorf("unsupported backend sort field %q", sortBy)
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
	connection, err := openBackendCommand(cmd)
	if err != nil {
		return err
	}
	var filtered []issuestore.Record
	var awaiting, ready map[string]bool
	var lines []string
	err = connection.ReadSnapshot(cmd.Context(), func(rows []backendstore.Issue) error {
		snapshot, e := issuestore.NewBackendQuerySnapshot(cmd.Context(), connection, rows)
		if e != nil {
			return e
		}
		if extra.mine {
			extra.implementer = connection.SessionID
		}
		selectedIDs := []string{}
		for _, raw := range ids {
			id := raw
			if len(id) != 19 || !strings.HasPrefix(id, "td-") {
				id, e = resolveBackendID(rows, raw)
				if e != nil {
					return e
				}
			}
			selectedIDs = append(selectedIDs, id)
		}
		parent, e := resolveBackendListHierarchy(cmd, connection, snapshot, rows, parentRaw, "parent")
		if e != nil {
			return e
		}
		epic, e := resolveBackendListHierarchy(cmd, connection, snapshot, rows, epicRaw, "epic")
		if e != nil {
			return e
		}
		byID := map[string]backendstore.Issue{}
		for _, row := range rows {
			byID[row.ID] = row
		}
		records := make([]issuestore.Record, 0, len(rows))
		for _, row := range rows {
			if parent != "" && row.ParentID != parent {
				continue
			}
			if epic != "" {
				match := false
				seen := map[string]bool{}
				for p := row.ParentID; p != ""; p = byID[p].ParentID {
					if seen[p] {
						return fmt.Errorf("cycle in backend hierarchy")
					}
					seen[p] = true
					if p == epic {
						match = true
						break
					}
				}
				if !match {
					continue
				}
			}
			records = append(records, issuestore.Record{Issue: row.Issue, Revision: row.Revision})
		}
		filtered = make([]issuestore.Record, 0)
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
			if len(selectedIDs) > 0 && !slices.Contains(selectedIDs, record.ID) {
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
			awaiting, ready, e = connection.ReviewBuckets(cmd.Context())
			if e != nil {
				return e
			}
			filtered = slices.DeleteFunc(filtered, func(r issuestore.Record) bool { return !awaiting[r.ID] && (!includeApproved || !ready[r.ID]) })
		}
		reverse, _ := cmd.Flags().GetBool("reverse")
		issues := make([]models.Issue, 0, len(filtered))
		byRecord := map[string]issuestore.Record{}
		for _, r := range filtered {
			issues = append(issues, r.Issue)
			byRecord[r.ID] = r
		}
		if e = issuestore.SortGitHubIssues(issues, sortBy, reverse); e != nil {
			return e
		}
		filtered = make([]issuestore.Record, 0, len(issues))
		for _, i := range issues {
			filtered = append(filtered, byRecord[i.ID])
		}
		if limit > 0 && len(filtered) > limit {
			filtered = filtered[:limit]
		}
		long, _ := cmd.Flags().GetBool("long")
		if (long || format == "long") && !jsonMode(cmd) && format != "json" {
			selected := make([]models.Issue, 0, len(filtered))
			for _, r := range filtered {
				selected = append(selected, r.Issue)
			}
			lines, e = backendListLines(snapshot, selected, true)
			if e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if jsonMode(cmd) || format == "json" {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(filtered)
	}
	long, _ := cmd.Flags().GetBool("long")
	if long || format == "long" {
		for _, line := range lines {
			cmd.Print(line)
		}
		if len(filtered) == 0 {
			cmd.Println("No issues found")
		}
		return nil
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

package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/output"
	"github.com/spf13/cobra"
	"strings"
)

type githubExportReader interface {
	ExportIssues(context.Context, bool, bool) ([]ghstore.ExportedIssue, error)
}

type githubJournalExportReader interface {
	ExportJournal(context.Context) (*ghstore.PortableJournalBundle, error)
}

func githubExport(cmd *cobra.Command, base string) (bool, error) {
	cfg, err := config.Load(base)
	if err != nil {
		return true, err
	}
	kind, err := config.Store(cfg)
	if err != nil {
		return true, err
	}
	format, _ := cmd.Flags().GetString("format")
	if kind != config.StoreGitHub {
		if format == "journal" {
			return true, fmt.Errorf("journal format requires store=gh-issue; no SQLite operation attempted")
		}
		return false, nil
	}
	if format != "json" && format != "md" && format != "journal" {
		return true, fmt.Errorf("unsupported export format %q", format)
	}
	if format == "journal" {
		if err := validateJournalExportFlags(cmd); err != nil {
			return true, err
		}
	}
	client, err := ghstore.Open(cmd.Context(), base, cfg.GitHub)
	if err != nil {
		return true, err
	}
	return true, runGitHubExport(cmd, client)
}
func runGitHubExport(cmd *cobra.Command, client githubExportReader) error {
	if err := validateExportOptions(cmd); err != nil {
		return err
	}
	all, _ := cmd.Flags().GetBool("all")
	format, _ := cmd.Flags().GetString("format")
	path, _ := cmd.Flags().GetString("output")
	render, _ := cmd.Flags().GetBool("render-markdown")
	if format == "journal" {
		if err := validateJournalExportFlags(cmd); err != nil {
			return err
		}
		reader, ok := client.(githubJournalExportReader)
		if !ok {
			return fmt.Errorf("portable journal export is unavailable")
		}
		bundle, err := reader.ExportJournal(cmd.Context())
		if err != nil {
			return err
		}
		data, err := ghstore.MarshalPortableJournal(bundle)
		if err != nil {
			return err
		}
		return writeExportOutput(cmd, path, data)
	}
	rows, err := client.ExportIssues(cmd.Context(), all, format == "json")
	if err != nil {
		return err
	}
	items := make([]exportedItem, 0, len(rows))
	var md strings.Builder
	md.WriteString("# Issues Export\n\n")
	for i := range rows {
		r := &rows[i]
		item := exportedItem{Issue: r.Record.Issue, GitHub: r, Logs: []models.Log{}, Handoffs: []models.Handoff{}, Dependencies: []models.IssueDependency{}, Files: []models.IssueFile{}}
		if r.Record.Details != nil {
			item.Files = append(item.Files, r.Record.Details.Files...)
			for _, dep := range r.Record.Details.Dependencies {
				item.Dependencies = append(item.Dependencies, models.IssueDependency{IssueID: r.Record.ID, DependsOnID: dep, RelationType: "depends_on"})
			}
		}
		for _, a := range r.Activity {
			switch a.Kind {
			case "log":
				item.Logs = append(item.Logs, models.Log{ID: a.ID, IssueID: a.IssueID, SessionID: a.SessionID, WorkSessionID: a.WorkSessionID, Message: a.Message, Type: a.LogType, Timestamp: a.CreatedAt})
			case "handoff":
				item.Handoffs = append(item.Handoffs, models.Handoff{ID: a.ID, IssueID: a.IssueID, SessionID: a.SessionID, Done: a.Done, Remaining: a.Remaining, Decisions: a.Decisions, Uncertain: a.Uncertain, Timestamp: a.CreatedAt})
			}
		}
		historicalLogs, historicalHandoffs, err := ghstore.ProjectImportedHistory(r.Activity, r.Record.ID)
		if err != nil {
			return err
		}
		ghstore.NamespacePortableHistorySessions(historicalLogs, historicalHandoffs)
		item.Logs = append(item.Logs, historicalLogs...)
		item.Handoffs = append(item.Handoffs, historicalHandoffs...)
		items = append(items, item)
		issue := r.Record.Issue
		fmt.Fprintf(&md, "## %s: %s\n\n- Status: %s\n- Type: %s\n- Priority: %s\n", issue.ID, issue.Title, issue.Status, issue.Type, issue.Priority)
		if issue.Points > 0 {
			fmt.Fprintf(&md, "- Points: %d\n", issue.Points)
		}
		if len(issue.Labels) > 0 {
			fmt.Fprintf(&md, "- Labels: %s\n", joinItems(issue.Labels))
		}
		if issue.Description != "" {
			fmt.Fprintf(&md, "\n%s\n", issue.Description)
		}
		md.WriteByte('\n')
	}
	var data []byte
	if format == "json" {
		data, err = json.MarshalIndent(items, "", "  ")
	} else {
		text := md.String()
		if render {
			text, err = output.RenderMarkdown(text)
		}
		data = []byte(text)
	}
	if err != nil {
		return err
	}
	return writeExportOutput(cmd, path, data)
}

func validateJournalExportFlags(cmd *cobra.Command) error {
	all, _ := cmd.Flags().GetBool("all")
	render, _ := cmd.Flags().GetBool("render-markdown")
	if all || render {
		return fmt.Errorf("journal format always preserves complete JSON history; omit --all and --render-markdown")
	}
	return nil
}

package cmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/output"
	"github.com/spf13/cobra"
)

func init() {
	for _, command := range []*cobra.Command{noteListCmd, noteShowCmd} {
		local, target := command.RunE, command
		command.RunE = func(cmd *cobra.Command, args []string) error {
			cfg, e := config.Load(getBaseDir())
			if e != nil {
				return e
			}
			kind, e := config.Store(cfg)
			if e != nil {
				return e
			}
			if kind == config.StoreBackend {
				return runBackendNoteRead(target, cmd, args)
			}
			return local(cmd, args)
		}
	}
}

func runBackendNoteRead(target, cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	c, e := openBackendCommand(cmd)
	if e != nil {
		return e
	}
	if target == noteShowCmd {
		includeDeleted, _ := cmd.Flags().GetBool("include-deleted")
		if includeDeleted {
			return fmt.Errorf("td-backend note show --include-deleted is not available yet")
		}
		rows, e := c.Notes(cmd.Context())
		if e != nil {
			return e
		}
		selected, e := backendstore.ResolveNote(rows, args[0])
		if e != nil {
			return e
		}
		n, e := c.Note(cmd.Context(), selected.ID)
		if e != nil {
			return e
		}
		if jsonMode(cmd) {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(n)
		}
		if _, e = fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", n.ID, n.Title); e != nil {
			return e
		}
		if n.Pinned {
			if _, e = fmt.Fprintln(cmd.OutOrStdout(), "Pinned: yes"); e != nil {
				return e
			}
		}
		if n.Archived {
			if _, e = fmt.Fprintln(cmd.OutOrStdout(), "Archived: yes"); e != nil {
				return e
			}
		}
		if _, e = fmt.Fprintf(cmd.OutOrStdout(), "Created: %s\nUpdated: %s\n", output.FormatTimeAgo(n.CreatedAt), output.FormatTimeAgo(n.UpdatedAt)); e != nil {
			return e
		}
		if n.Content != "" {
			_, e = fmt.Fprintf(cmd.OutOrStdout(), "\n%s\n", n.Content)
		}
		return e
	}
	deleted, _ := cmd.Flags().GetBool("deleted")
	if deleted {
		return fmt.Errorf("td-backend note list --deleted is not available yet")
	}
	rows, e := c.Notes(cmd.Context())
	if e != nil {
		return e
	}
	pinned, _ := cmd.Flags().GetBool("pinned")
	archived, _ := cmd.Flags().GetBool("archived")
	all, _ := cmd.Flags().GetBool("all")
	search, _ := cmd.Flags().GetString("search")
	limit, _ := cmd.Flags().GetInt("limit")
	search = strings.ToLower(strings.TrimSpace(search))
	filtered := []backendstore.Note{}
	for _, n := range rows {
		if pinned && !n.Pinned || archived && !n.Archived || !archived && !all && n.Archived {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(n.Title+"\n"+n.Content), search) {
			continue
		}
		filtered = append(filtered, n)
		if limit > 0 && len(filtered) >= limit {
			break
		}
	}
	format, _ := cmd.Flags().GetString("output")
	if jsonMode(cmd) || format == "json" {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(filtered)
	}
	if len(filtered) == 0 {
		_, e = fmt.Fprintln(cmd.OutOrStdout(), "No notes found")
		return e
	}
	for _, n := range filtered {
		pin := " "
		if n.Pinned {
			pin = "*"
		}
		title := n.Title
		runes := []rune(title)
		if len(runes) > 50 {
			title = string(runes[:47]) + "..."
		}
		if _, e = fmt.Fprintf(cmd.OutOrStdout(), "%s %s  %-50s  %s  %s\n", pin, n.ID, title, output.FormatTimeAgo(n.CreatedAt), output.FormatTimeAgo(n.UpdatedAt)); e != nil {
			return e
		}
	}
	return nil
}

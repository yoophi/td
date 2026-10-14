package cmd

import (
	"encoding/json"
	"fmt"
	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/output"
	"github.com/spf13/cobra"
	"strings"
)

func backendInfo(cmd *cobra.Command, base string) (bool, error) {
	cfg, e := config.Load(base)
	if e != nil {
		return true, e
	}
	kind, e := config.Store(cfg)
	if e != nil {
		return true, e
	}
	if kind != config.StoreBackend {
		return false, nil
	}
	cmd.SilenceUsage = true
	c, e := openBackendCommand(cmd)
	if e != nil {
		return true, e
	}
	var stats backendstore.Stats
	var project backendstore.Project
	var awaiting, ready map[string]bool
	e = c.ReadConsistent(cmd.Context(), func() error {
		var e error
		stats, e = c.Stats(cmd.Context())
		if e != nil {
			return e
		}
		project, e = c.Client.GetProject(cmd.Context(), c.ProjectID, c.SessionID)
		if e != nil {
			return e
		}
		awaiting, ready, e = c.ReviewBuckets(cmd.Context())
		return e
	})
	if e != nil {
		return true, e
	}
	issues := map[string]int{"total": stats.Total}
	for _, key := range []string{"open", "in_progress", "blocked", "in_review", "closed"} {
		issues[key] = stats.ByStatus[key]
	}
	queue := map[string]int{"awaiting_review": issues["in_review"], "you_can_review": len(awaiting), "you_can_close_after": len(ready)}
	cmd.SetOut(cmd.OutOrStdout())
	if jsonMode(cmd) {
		return true, json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"project": project.Name, "base_dir": base, "store": config.StoreBackend, "backend": c.Association(), "current_session": c.SessionID, "issues": issues, "by_type": stats.ByType, "by_priority": stats.ByPriority, "review_queue": queue})
	}
	var text strings.Builder
	fmt.Fprintf(&text, "Project: %s\nStore: td-backend\nBackend URL: %s\nProject ID: %s\nCurrent Session: %s\n\nIssues: %d total\n", output.SanitizeIssueText(project.Name), output.SanitizeIssueText(c.Client.URL), c.ProjectID, c.SessionID, stats.Total)
	for _, section := range []struct {
		name   string
		keys   []string
		values map[string]int
	}{{"By Status", []string{"open", "in_progress", "blocked", "in_review", "closed"}, issues}, {"Review Queue", []string{"awaiting_review", "you_can_review", "you_can_close_after"}, queue}, {"By Type", []string{"bug", "feature", "task", "epic", "chore"}, stats.ByType}, {"By Priority", []string{"P0", "P1", "P2", "P3", "P4"}, stats.ByPriority}} {
		fmt.Fprintf(&text, "\n%s:\n", section.name)
		for _, key := range section.keys {
			fmt.Fprintf(&text, "  %s: %d\n", key, section.values[key])
		}
	}
	_, e = fmt.Fprint(cmd.OutOrStdout(), text.String())
	return true, e
}

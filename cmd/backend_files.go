package cmd

import (
	"fmt"

	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/internal/ghstore"
	"github.com/spf13/cobra"
)

func runBackendFiles(original, cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	cmd.SetOut(cmd.OutOrStdout())
	if original != filesCmd {
		return fmt.Errorf("td-backend does not yet support %s", original.Name())
	}
	if len(args) != 1 {
		return fmt.Errorf("one issue ID required")
	}
	c, e := openBackendCommand(cmd)
	if e != nil {
		return e
	}
	var detail backendstore.Detail
	e = c.ReadSnapshot(cmd.Context(), func(rows []backendstore.Issue) error {
		id, e := resolveBackendID(rows, args[0])
		if e != nil {
			return e
		}
		detail, e = c.Detail(cmd.Context(), id)
		return e
	})
	if e != nil {
		return e
	}
	record := &ghstore.Record{Issue: detail.Issue.Issue, Details: &ghstore.IssueDetails{Files: detail.Files}}
	return listGitHubFiles(cmd, record, c.Worktree)
}

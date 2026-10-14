package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/output"
	"github.com/spf13/cobra"
)

func runBackendDeleted(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	if len(args) != 0 {
		return fmt.Errorf("deleted does not accept positional arguments")
	}
	c, err := openBackendCommand(cmd)
	if err != nil {
		return err
	}
	rows, err := c.Client.ListIssuesIncludingDeleted(cmd.Context(), c.ProjectID, c.SessionID)
	if err != nil {
		return err
	}
	issues := []models.Issue{}
	for _, row := range rows {
		if row.DeletedAt != nil {
			issues = append(issues, row.Issue)
		}
	}
	if jsonMode(cmd) {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(issues)
	}
	for _, issue := range issues {
		if _, err := fmt.Fprintln(cmd.OutOrStdout(), output.FormatIssueDeleted(&issue)); err != nil {
			return err
		}
	}
	if len(issues) == 0 {
		_, err = fmt.Fprintln(cmd.OutOrStdout(), "No deleted issues")
	}
	return err
}

package cmd

import (
	"fmt"
	"strings"

	"github.com/marcus/td/internal/db"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/output"
	"github.com/marcus/td/internal/session"
	"github.com/spf13/cobra"
)

var deleteCmd = &cobra.Command{
	Use:     "delete [issue-id...]",
	Short:   "Soft-delete one or more issues",
	GroupID: "core",
	Args:    cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		reason, err := deletionReason(cmd)
		if err != nil {
			return err
		}
		baseDir := getBaseDir()

		database, err := db.Open(baseDir)
		if err != nil {
			output.Error("%v", err)
			return err
		}
		defer func() { _ = database.Close() }()

		sess, _ := session.GetOrCreate(database)
		sessionID := ""
		if sess != nil {
			sessionID = sess.ID
		}

		for _, issueID := range args {
			if err := database.DeleteIssueLogged(issueID, sessionID); err != nil {
				output.Error("failed to delete %s: %v", issueID, err)
				continue
			}

			if reason != "" {
				if err := database.AddLog(&models.Log{IssueID: issueID, SessionID: sessionID, Type: models.LogTypeProgress, Message: "Deleted: " + reason}); err != nil {
					return fmt.Errorf("%s was deleted but reason logging failed; inspect current state before retrying: %w", issueID, err)
				}
			}
			fmt.Printf("DELETED %s\n", issueID)
		}

		return nil
	},
}

var restoreCmd = &cobra.Command{
	Use:     "restore [issue-id...]",
	Short:   "Restore soft-deleted issues",
	GroupID: "core",
	Args:    cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		reason, err := deletionReason(cmd)
		if err != nil {
			return err
		}
		baseDir := getBaseDir()

		database, err := db.Open(baseDir)
		if err != nil {
			output.Error("%v", err)
			return err
		}
		defer func() { _ = database.Close() }()

		sess, _ := session.GetOrCreate(database)
		sessionID := ""
		if sess != nil {
			sessionID = sess.ID
		}

		for _, issueID := range args {
			if err := database.RestoreIssueLogged(issueID, sessionID); err != nil {
				output.Error("failed to restore %s: %v", issueID, err)
				continue
			}

			if reason != "" {
				if err := database.AddLog(&models.Log{IssueID: issueID, SessionID: sessionID, Type: models.LogTypeProgress, Message: "Restored: " + reason}); err != nil {
					return fmt.Errorf("%s was restored but reason logging failed; inspect current state before retrying: %w", issueID, err)
				}
			}
			fmt.Printf("RESTORED %s\n", issueID)
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(deleteCmd)
	rootCmd.AddCommand(restoreCmd)
	deleteCmd.Flags().String("reason", "", "Reason for logical deletion")
	restoreCmd.Flags().String("reason", "", "Reason for restoring a deleted issue")

	// Accept --force and --yes as no-ops for LLM compatibility
	deleteCmd.Flags().BoolP("force", "f", false, "No-op (delete always succeeds)")
	deleteCmd.Flags().BoolP("yes", "y", false, "No-op (delete always succeeds, alias for --force)")
}

// Explicit empty acknowledgements should not silently become absent reasons.
func deletionReason(cmd *cobra.Command) (string, error) {
	reason, _ := cmd.Flags().GetString("reason")
	reason = strings.TrimSpace(reason)
	if cmd.Flags().Changed("reason") && reason == "" {
		return "", fmt.Errorf("--reason requires a nonblank value")
	}
	return reason, nil
}

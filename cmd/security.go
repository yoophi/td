package cmd

import (
	"encoding/json"

	"github.com/marcus/td/internal/auditlog"
	"github.com/marcus/td/internal/output"
	"github.com/spf13/cobra"
)

var securityCmd = &cobra.Command{
	Use:     "security",
	Short:   "View security exception log (review/close exceptions)",
	Long:    `Shows device-local audit records of review/close exceptions. GitHub writes record attempted and confirmed or uncertain outcomes; clearing this file retains shared review history.`,
	GroupID: "system",
	RunE: func(cmd *cobra.Command, args []string) error {
		baseDir := getBaseDir()

		jsonOut, _ := cmd.Flags().GetBool("json")
		clearFlag, _ := cmd.Flags().GetBool("clear")
		if clearFlag {
			if err := auditlog.ClearSecurityEvents(baseDir); err != nil {
				output.Error("failed to clear security events: %v", err)
				return err
			}
			if jsonOut {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"action": "clear", "scope": "device-local", "cleared": true})
			}
			cmd.Println("Cleared device-local security exception log (shared review history retained)")
			return nil
		}

		events, err := auditlog.ReadSecurityEvents(baseDir)
		if err != nil {
			output.Error("failed to read security events: %v", err)
			return err
		}

		if jsonOut {
			encoder := json.NewEncoder(cmd.OutOrStdout())
			for _, event := range events {
				view := struct {
					auditlog.SecurityEvent
					AgentType string `json:"agent_type"`
				}{SecurityEvent: event, AgentType: event.AgentType}
				if err := encoder.Encode(view); err != nil {
					return err
				}
			}
			return nil
		}
		if len(events) == 0 {
			cmd.Println("No device-local security exceptions logged")
			return nil
		}
		// Human-readable output
		cmd.Printf("Security Exceptions (%d, device-local):\n\n", len(events))
		for _, e := range events {
			ts := e.Timestamp.Local().Format("2006-01-02 15:04:05")
			agent := e.AgentType
			if agent == "" {
				agent = "unknown"
			}

			cmd.Printf("%s  %s (Agent: %s)\n", ts, e.IssueID, agent)
			cmd.Printf("  Reason: %s\n", e.Reason)
			if e.SessionID != "" {
				cmd.Printf("  Session: %s\n", e.SessionID)
			}
			if e.Outcome != "" {
				cmd.Printf("  Outcome: %s (operation %s, repository %s)\n", e.Outcome, e.OperationID, e.Repository)
			}
			cmd.Println()
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(securityCmd)

	securityCmd.Flags().Bool("clear", false, "Clear device-local audit records (retain shared reviews)")
	securityCmd.Flags().Bool("json", false, "Output as JSONL")
}

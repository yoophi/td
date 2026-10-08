package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

var taskCmd = &cobra.Command{
	Use:     "task",
	Short:   "Shortcuts for working with tasks",
	Long:    `Convenience commands for creating and viewing tasks.`,
	GroupID: "core",
}

var taskCreateCmd = &cobra.Command{
	Use:   "create [title]",
	Short: "Create a new task",
	Long: `Create a new task. Shorthand for 'td add --type task'.

Examples:
  td task create "Implement login endpoint"
  td task create "Fix auth bug" --priority P1`,
	Args: func(cmd *cobra.Command, args []string) error {
		title, _ := cmd.Flags().GetString("title")
		if len(args) == 0 && title == "" {
			return fmt.Errorf("requires a title argument or --title flag")
		}
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		// Set type=task on this command's flags so createCmd.RunE reads it correctly
		if err := cmd.Flags().Set("type", "task"); err != nil {
			return err
		}
		if len(args) == 0 {
			title, _ := cmd.Flags().GetString("title")
			args = []string{title}
		}
		return createCmd.RunE(cmd, args)
	},
}

var taskListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all tasks",
	Long:  `List all tasks. Shorthand for 'td list --type task'.`,
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Replace rather than append: repeated invocations must keep a fixed type.
		if err := cmd.Flags().Lookup("type").Value.(pflag.SliceValue).Replace([]string{"task"}); err != nil {
			return err
		}
		return listCmd.RunE(cmd, args)
	},
}

func init() {
	rootCmd.AddCommand(taskCmd)
	taskCmd.AddCommand(taskCreateCmd)
	taskCmd.AddCommand(taskListCmd)

	registerCreateFlags(taskCreateCmd)
	_ = taskCreateCmd.Flags().MarkHidden("type")
	registerListFlags(taskListCmd)
	_ = taskListCmd.Flags().MarkHidden("type")
}

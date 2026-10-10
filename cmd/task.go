package cmd

import (
	"fmt"
	"strings"

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
The issue type is fixed; a conflicting --type is rejected.

Examples:
  td task create "Implement login endpoint"
  td task create "Fix auth bug" --priority P1`,
	Args: func(cmd *cobra.Command, args []string) error {
		if err := validateShortcutType(cmd, "task", false); err != nil {
			return err
		}
		_, _, err := resolveCreateInput(cmd, args, "task")
		return err
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		// Set type=task on this command's flags so createCmd.RunE reads it correctly
		if err := cmd.Flags().Set("type", "task"); err != nil {
			return err
		}
		return createCmd.RunE(cmd, args)
	},
}

var taskListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all tasks",
	Long: `List all tasks. Shorthand for 'td list --type task'. The type filter is
fixed; a conflicting --type is rejected. --filter always includes type=task.
Do not combine TDQ with other ordinary selection flags.`,
	Args: func(cmd *cobra.Command, args []string) error {
		if err := cobra.NoArgs(cmd, args); err != nil {
			return err
		}
		if err := validateShortcutType(cmd, "task", true); err != nil {
			return err
		}
		_, err := listQueryInput(cmd, args, "task")
		return err
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		// Replace rather than append: repeated invocations must keep a fixed type.
		setFixedListType(cmd, "task")
		if err := cmd.Flags().Lookup("type").Value.(pflag.SliceValue).Replace([]string{"task"}); err != nil {
			return err
		}
		return listCmd.RunE(cmd, args)
	},
}

func validateShortcutType(cmd *cobra.Command, fixed string, list bool) error {
	if !cmd.Flags().Changed("type") {
		return nil
	}
	var values []string
	var err error
	if list {
		values, err = cmd.Flags().GetStringArray("type")
	} else {
		var value string
		value, err = cmd.Flags().GetString("type")
		values = []string{value}
	}
	if err != nil {
		return err
	}
	if len(values) == 0 {
		return fmt.Errorf("%s fixes --type to %s; --type must not be empty", cmd.CommandPath(), fixed)
	}
	for _, value := range values {
		types := []string{value}
		if list {
			types = strings.Split(value, ",")
		}
		for _, typ := range types {
			if strings.ToLower(strings.TrimSpace(typ)) != fixed {
				return fmt.Errorf("%s fixes --type to %s; use 'td create' or 'td list' to select another type", cmd.CommandPath(), fixed)
			}
		}
	}
	return nil
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

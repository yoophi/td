package cmd

import (
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

var epicCmd = &cobra.Command{
	Use:     "epic",
	Short:   "Shortcuts for working with epics",
	Long:    `Convenience commands for creating and viewing epics.`,
	GroupID: "core",
}

var epicCreateCmd = &cobra.Command{
	Use:   "create [title]",
	Short: "Create a new epic",
	Long: `Create a new epic. Shorthand for 'td add --type epic'.
The issue type is fixed; a conflicting --type is rejected.

Examples:
  td epic create "Multi-user support"
  td epic create "Auth system" --priority P0`,
	Args: func(cmd *cobra.Command, args []string) error {
		if err := validateShortcutType(cmd, "epic", false); err != nil {
			return err
		}
		_, _, err := resolveCreateInput(cmd, args, "epic")
		return err
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		// Set type=epic on this command's flags so createCmd.RunE reads it correctly
		if err := cmd.Flags().Set("type", "epic"); err != nil {
			return err
		}
		return createCmd.RunE(cmd, args)
	},
}

var epicListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all epics",
	Long:  `List all epics. Shorthand for 'td list --type epic'. The type filter is fixed; a conflicting --type is rejected.`,
	Args: func(cmd *cobra.Command, args []string) error {
		if err := cobra.NoArgs(cmd, args); err != nil {
			return err
		}
		return validateShortcutType(cmd, "epic", true)
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		// Replace rather than append: repeated invocations must keep a fixed type.
		if err := cmd.Flags().Lookup("type").Value.(pflag.SliceValue).Replace([]string{"epic"}); err != nil {
			return err
		}
		return listCmd.RunE(cmd, args)
	},
}

func init() {
	rootCmd.AddCommand(epicCmd)
	epicCmd.AddCommand(epicCreateCmd)
	epicCmd.AddCommand(epicListCmd)

	registerCreateFlags(epicCreateCmd)
	_ = epicCreateCmd.Flags().MarkHidden("type")
	registerListFlags(epicListCmd)
	_ = epicListCmd.Flags().MarkHidden("type")
}

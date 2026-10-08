package cmd

import (
	"fmt"

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

Examples:
  td epic create "Multi-user support"
  td epic create "Auth system" --priority P0`,
	Args: func(cmd *cobra.Command, args []string) error {
		title, _ := cmd.Flags().GetString("title")
		if len(args) == 0 && title == "" {
			return fmt.Errorf("requires a title argument or --title flag")
		}
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		// Set type=epic on this command's flags so createCmd.RunE reads it correctly
		if err := cmd.Flags().Set("type", "epic"); err != nil {
			return err
		}
		if len(args) == 0 {
			title, _ := cmd.Flags().GetString("title")
			args = []string{title}
		}
		return createCmd.RunE(cmd, args)
	},
}

var epicListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all epics",
	Long:  `List all epics. Shorthand for 'td list --type epic'.`,
	Args:  cobra.NoArgs,
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

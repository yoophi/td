package cmd

import (
	"encoding/json"
	"sort"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

type helpJSONFlag struct {
	Name       string `json:"name"`
	Shorthand  string `json:"shorthand,omitempty"`
	Type       string `json:"type"`
	Usage      string `json:"usage"`
	Default    string `json:"default"`
	Hidden     bool   `json:"hidden"`
	Deprecated string `json:"deprecated,omitempty"`
	Inherited  bool   `json:"inherited"`
}

type helpJSONCommand struct {
	Command  string   `json:"command"`
	Usage    string   `json:"usage"`
	Short    string   `json:"short"`
	Long     string   `json:"long"`
	Example  string   `json:"example"`
	Aliases  []string `json:"aliases"`
	Hidden   bool     `json:"hidden"`
	Runnable bool     `json:"runnable"`
}

func commandHelpJSON(cmd *cobra.Command) helpJSONCommand {
	aliases := append([]string{}, cmd.Aliases...)
	sort.Strings(aliases)
	return helpJSONCommand{Command: cmd.CommandPath(), Usage: cmd.UseLine(),
		Short: cmd.Short, Long: cmd.Long, Example: cmd.Example,
		Aliases: aliases, Hidden: cmd.Hidden, Runnable: cmd.Runnable()}
}

func printJSONHelp(cmd *cobra.Command) error {
	flags := []helpJSONFlag{}
	cmd.Flags().VisitAll(func(flag *pflag.Flag) {
		flags = append(flags, helpJSONFlag{Name: flag.Name, Shorthand: flag.Shorthand,
			Type: flag.Value.Type(), Usage: flag.Usage, Default: flag.DefValue,
			Hidden: flag.Hidden, Deprecated: flag.Deprecated,
			Inherited: cmd.InheritedFlags().Lookup(flag.Name) != nil})
	})
	sort.Slice(flags, func(i, j int) bool { return flags[i].Name < flags[j].Name })
	children := []helpJSONCommand{}
	for _, child := range cmd.Commands() {
		children = append(children, commandHelpJSON(child))
	}
	return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
		helpJSONCommand
		Flags    []helpJSONFlag    `json:"flags"`
		Commands []helpJSONCommand `json:"commands"`
	}{commandHelpJSON(cmd), flags, children})
}

func init() {
	humanHelp := rootCmd.HelpFunc()
	rootCmd.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		if jsonMode(cmd) {
			if err := printJSONHelp(cmd); err != nil {
				cmd.PrintErrln("Error writing JSON help:", err)
			}
			return
		}
		humanHelp(cmd, args)
	})
}

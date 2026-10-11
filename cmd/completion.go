package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

func configureCompletion() {
	// Keep Cobra's command names, flags and shell-specific help. Its generated
	// handlers capture a writer and ignore our persistent JSON flag, so bind
	// the generators to the invocation's writer and explicitly encode JSON.
	rootCmd.InitDefaultCompletionCmd()
	for _, command := range rootCmd.Commands() {
		if command.Name() != "completion" {
			continue
		}
		// Cobra's non-runnable parent otherwise treats an unknown shell as a
		// successful help request. Keep no-argument help, but validate extras.
		command.Args = cobra.NoArgs
		command.RunE = func(cmd *cobra.Command, _ []string) error { return cmd.Help() }
		for _, shell := range command.Commands() {
			shell.RunE = runCompletion
		}
	}
}

func runCompletion(cmd *cobra.Command, _ []string) error {
	noDescriptions, _ := cmd.Flags().GetBool("no-descriptions")
	out := cmd.OutOrStdout()
	var script bytes.Buffer
	if jsonMode(cmd) {
		out = &script
	}
	if err := generateCompletion(cmd.Root(), cmd.Name(), out, noDescriptions); err != nil {
		return err
	}
	if jsonMode(cmd) {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
			Shell  string `json:"shell"`
			Script string `json:"script"`
		}{Shell: cmd.Name(), Script: script.String()})
	}
	return nil
}

func generateCompletion(root *cobra.Command, shell string, out io.Writer, noDescriptions bool) error {
	switch shell {
	case "bash":
		return root.GenBashCompletionV2(out, !noDescriptions)
	case "zsh":
		if noDescriptions {
			return root.GenZshCompletionNoDesc(out)
		}
		return root.GenZshCompletion(out)
	case "fish":
		return root.GenFishCompletion(out, !noDescriptions)
	case "powershell":
		if noDescriptions {
			return root.GenPowerShellCompletion(out)
		}
		return root.GenPowerShellCompletionWithDesc(out)
	default:
		return fmt.Errorf("unsupported completion shell: %s", shell)
	}
}

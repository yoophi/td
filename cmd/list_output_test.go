package cmd

import (
	"testing"

	"github.com/spf13/cobra"
)

func TestListOutputOptionsValidatedBeforeExecution(t *testing.T) {
	for _, args := range [][]string{
		{"--format", "xml"},
		{"--filter", "type = task", "--format", "xml"},
		{"--short", "--long"},
		{"--format", "short", "--long"},
		{"--format", "long", "--short"},
		{"--limit", "-1"},
	} {
		cmd := shortcutTestCommand(listCmd, false)
		ran := false
		cmd.RunE = func(*cobra.Command, []string) error { ran = true; return nil }
		cmd.SetArgs(args)
		if err := cmd.Execute(); err == nil || ran {
			t.Fatal(args, err, ran)
		}
	}
	for _, args := range [][]string{
		{"--format", "short"},
		{"--format", "long"},
		{"--format", "json", "--short", "--long"},
		{"--json", "--format", "long", "--short"},
		{"--long", "--short=false"},
		{"--limit", "0"},
	} {
		cmd := shortcutTestCommand(listCmd, false)
		ran := false
		cmd.RunE = func(*cobra.Command, []string) error { ran = true; return nil }
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil || !ran {
			t.Fatal(args, err, ran)
		}
	}
}

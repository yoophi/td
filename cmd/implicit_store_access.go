package cmd

import "github.com/spf13/cobra"

// These commands own their explicit I/O (e.g. config store validation or
// version --check). Implicit issue autosync/session discovery is unrelated.
func skipImplicitIssueAccess(cmd *cobra.Command) bool {
	if cmd == nil {
		// Cobra can return without running any command for root help/version
		// or dispatch errors. There is no executed issue operation to identify.
		return true
	}
	if help, _ := cmd.Flags().GetBool("help"); help {
		return true
	}
	top := cmd
	for top.Parent() != nil && top.Parent().Name() != "td" {
		top = top.Parent()
	}
	switch top.Name() {
	case "config", "feature", "workflow", "debug-stats", "version", "completion", "help", "init", "errors", "security":
		return true
	case "stats":
		if cmd.Parent() == top {
			switch cmd.Name() {
			case "analytics", "errors", "security":
				return true
			}
		}
	case "query":
		for _, name := range []string{"fields", "examples", "explain"} {
			if enabled, _ := cmd.Flags().GetBool(name); enabled {
				return true
			}
		}
	}
	return false
}

func diagnosticCommandSessionID(cmd *cobra.Command, dir string) string {
	if skipImplicitIssueAccess(cmd) {
		return ""
	}
	return diagnosticSessionID(dir)
}

package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

type fixedListTypeKey struct{}

func setFixedListType(cmd *cobra.Command, typ string) {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	cmd.SetContext(context.WithValue(ctx, fixedListTypeKey{}, typ))
}

func fixedListType(cmd *cobra.Command) string {
	if cmd.Context() == nil {
		return ""
	}
	typ, _ := cmd.Context().Value(fixedListTypeKey{}).(string)
	return typ
}

// TDQ owns selection; ordinary list filters must not be silently discarded.
// Fixed-type shortcuts carry their mandatory constraint through the dispatch
// callback's context without rewriting the caller's query flags.
func listQueryInput(cmd *cobra.Command, args []string, fixed string) (string, error) {
	if err := validateListOutput(cmd); err != nil {
		return "", err
	}
	expression, _ := cmd.Flags().GetString("filter")
	positional := strings.TrimSpace(strings.Join(args, " "))
	if cmd.Flags().Changed("filter") && strings.TrimSpace(expression) == "" {
		return "", fmt.Errorf("--filter requires a non-empty query expression")
	}
	if expression != "" && positional != "" {
		return "", fmt.Errorf("cannot use both --filter and positional query")
	}
	if positional != "" {
		expression = positional
	}
	if expression == "" {
		return "", nil
	}
	for _, name := range []string{"id", "status", "type", "labels", "priority", "points", "search", "implementer", "reviewer", "reviewable", "include-approved", "parent", "epic", "mine", "open", "created", "updated", "closed", "all", "deferred", "overdue", "surfacing", "due-soon"} {
		if name == "type" && fixed != "" {
			continue // The shortcut separately validates explicit type values.
		}
		if cmd.Flags().Changed(name) {
			return "", fmt.Errorf("--%s cannot be combined with a TDQ list query; put selection conditions in the query or use ordinary list filters", name)
		}
	}
	if fixed != "" {
		expression = "type = " + fixed + " AND (" + expression + ")"
	}
	return expression, nil
}

func validateListOutput(cmd *cobra.Command) error {
	format, _ := cmd.Flags().GetString("format")
	switch format {
	case "", "short", "long", "json":
	default:
		return fmt.Errorf("unsupported output format %q (use short, long or json)", format)
	}
	limit, _ := cmd.Flags().GetInt("limit")
	if limit < 0 {
		return fmt.Errorf("limit must be zero (unlimited) or positive")
	}
	// JSON has documented precedence over valid human display modifiers.
	if jsonMode(cmd) || format == "json" {
		return nil
	}
	short, _ := cmd.Flags().GetBool("short")
	long, _ := cmd.Flags().GetBool("long")
	if short && (long || format == "long") || long && format == "short" {
		return fmt.Errorf("conflicting short/long list output options; choose one display format")
	}
	return nil
}

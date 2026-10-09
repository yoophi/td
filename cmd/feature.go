package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/features"
	"github.com/marcus/td/internal/output"
	"github.com/marcus/td/internal/reviewpolicy"
	"github.com/spf13/cobra"
)

// isStringFeature reports whether name is a string-valued (non-boolean)
// feature flag. Currently only review_policy_mode is string-valued; the
// boolean feature surface (sync_cli, balanced_review_policy, ...) is handled
// by the generic registry.
func isStringFeature(name string) bool {
	return name == features.ReviewPolicyMode
}

var featureCmd = &cobra.Command{
	Use:     "feature",
	Short:   "Manage experimental feature flags",
	GroupID: "system",
}

// featureListEntry is one row of `td feature list`, in the shape --json emits.
// The columns of the human table are the fields: state is the resolved value
// as a string ("on"/"off" for the boolean registry, the mode name for the
// string-valued review_policy_mode) so both kinds of flag share one row shape.
// enabled carries the boolean reading for the flags that have one and is null
// for the string-valued flag: "not applicable" rather than a misleading false.
// It is deliberately NOT omitempty — every row must carry every key, so a
// caller can read row["enabled"] on any row without a field-existence check.
// Key absence would be a third rendering of "empty" alongside null and false.
type featureListEntry struct {
	Name        string `json:"name"`
	State       string `json:"state"`
	Enabled     *bool  `json:"enabled"`
	Source      string `json:"source"`
	Description string `json:"description"`
}

var featureListCmd = &cobra.Command{
	Use:   "list",
	Short: "List known feature flags and their resolved state",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		baseDir := getBaseDir()

		var entries []featureListEntry
		for _, feature := range features.ListAll() {
			enabled, source := features.Resolve(baseDir, feature.Name)
			state := "off"
			if enabled {
				state = "on"
			}
			enabledVal := enabled
			entries = append(entries, featureListEntry{
				Name:        feature.Name,
				State:       state,
				Enabled:     &enabledVal,
				Source:      source,
				Description: feature.Description,
			})
		}

		// review_policy_mode is string-valued and lives outside the boolean
		// registry; surface it here so `td feature list` shows the resolved
		// review policy alongside the boolean flags.
		mode, source, err := reviewPolicyFeatureState(baseDir)
		if err != nil {
			return err
		}
		entries = append(entries, featureListEntry{
			Name:        features.ReviewPolicyMode,
			State:       string(mode),
			Source:      source,
			Description: "Review policy: strict|balanced|delegated|trusted (default: trusted)",
		})

		if jsonMode(cmd) {
			return output.JSON(jsonList(entries))
		}

		fmt.Printf("%-22s  %-9s  %-7s  %s\n", "NAME", "STATE", "SOURCE", "DESCRIPTION")
		for _, entry := range entries {
			fmt.Printf("%-22s  %-9s  %-7s  %s\n", entry.Name, entry.State, entry.Source, entry.Description)
		}

		return nil
	},
}

var featureGetCmd = &cobra.Command{
	Use:   "get <name>",
	Short: "Get a feature flag state",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := normalizeFeatureArg(args[0])

		if isStringFeature(name) {
			mode, source, err := reviewPolicyFeatureState(getBaseDir())
			if err != nil {
				output.Error("%v", err)
				return err
			}
			if jsonMode(cmd) {
				return output.JSON(map[string]any{"name": name, "value": mode, "source": source})
			}
			fmt.Printf("%s=%s (source=%s)\n", name, mode, source)
			return nil
		}

		if !features.IsKnownFeature(name) {
			return unknownFeatureError(name)
		}

		enabled, source := features.Resolve(getBaseDir(), name)
		if jsonMode(cmd) {
			return output.JSON(map[string]any{"name": name, "value": enabled, "source": source})
		}
		fmt.Printf("%s=%t (source=%s)\n", name, enabled, source)
		return nil
	},
}

var featureSetCmd = &cobra.Command{
	Use:   "set <name> <value>",
	Short: "Set a feature flag in local project config",
	Long: `Set a stored project override. Boolean features accept true/false,
on/off, yes/no or 1/0. review_policy_mode accepts strict, balanced, delegated
or trusted. Environment overrides still take precedence; use feature get to
inspect the effective value and source.`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := normalizeFeatureArg(args[0])

		if isStringFeature(name) {
			raw := strings.ToLower(strings.TrimSpace(args[1]))
			mode, err := reviewpolicy.ParseMode(raw)
			if err != nil {
				output.Error("%v", err)
				return err
			}
			if err := config.SetFeatureStringFlag(getBaseDir(), name, string(mode)); err != nil {
				output.Error("set feature flag: %v", err)
				return err
			}
			return reportFeatureMutation(cmd, "set", name, string(mode))
		}

		if !features.IsKnownFeature(name) {
			return unknownFeatureError(name)
		}

		enabled, err := parseBoolString(args[1])
		if err != nil {
			output.Error("%v", err)
			return err
		}

		if err := config.SetFeatureFlag(getBaseDir(), name, enabled); err != nil {
			output.Error("set feature flag: %v", err)
			return err
		}

		return reportFeatureMutation(cmd, "set", name, enabled)
	},
}

var featureUnsetCmd = &cobra.Command{
	Use:   "unset <name>",
	Short: "Remove a local feature flag override",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := normalizeFeatureArg(args[0])

		if isStringFeature(name) {
			if err := config.UnsetFeatureStringFlag(getBaseDir(), name); err != nil {
				output.Error("unset feature flag: %v", err)
				return err
			}
			return reportFeatureMutation(cmd, "unset", name, nil)
		}

		if !features.IsKnownFeature(name) {
			return unknownFeatureError(name)
		}

		if err := config.UnsetFeatureFlag(getBaseDir(), name); err != nil {
			output.Error("unset feature flag: %v", err)
			return err
		}

		return reportFeatureMutation(cmd, "unset", name, nil)
	},
}

// Report the source using the resolver's precedence, including legacy flags.
// Resolution errors must not be hidden by presenting an incomplete inventory.
func reviewPolicyFeatureState(baseDir string) (reviewpolicy.Mode, string, error) {
	mode, err := features.ResolveReviewPolicyMode(baseDir)
	if err != nil {
		return "", "", err
	}
	if strings.TrimSpace(os.Getenv("TD_FEATURE_REVIEW_POLICY_MODE")) != "" {
		return mode, "env", nil
	}
	if _, ok, _ := config.GetFeatureStringFlag(baseDir, features.ReviewPolicyMode); ok {
		return mode, "config", nil
	}
	_, source := features.Resolve(baseDir, features.BalancedReviewPolicy.Name)
	return mode, source, nil
}

func reportFeatureMutation(cmd *cobra.Command, action, name string, value any) error {
	if jsonMode(cmd) {
		result := map[string]any{"action": action, "name": name, "scope": "project"}
		if action == "set" {
			result["value"] = value // Stored override, not an env-overridden effective value.
		}
		return output.JSON(result)
	}
	if action == "set" {
		output.Success("feature %s set to %v", name, value)
	} else {
		output.Success("feature %s unset", name)
	}
	return nil
}

func normalizeFeatureArg(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func parseBoolString(raw string) (bool, error) {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	switch normalized {
	case "1", "true", "on", "yes":
		return true, nil
	case "0", "false", "off", "no":
		return false, nil
	default:
		return false, fmt.Errorf("invalid bool value %q", raw)
	}
}

func unknownFeatureError(name string) error {
	var names []string
	for _, feature := range features.ListAll() {
		names = append(names, feature.Name)
	}
	names = append(names, features.ReviewPolicyMode)
	return fmt.Errorf("unknown feature %q (known: %s)", name, strings.Join(names, ", "))
}

func init() {
	featureCmd.AddCommand(featureListCmd)
	featureCmd.AddCommand(featureGetCmd)
	featureCmd.AddCommand(featureSetCmd)
	featureCmd.AddCommand(featureUnsetCmd)

	featureSetCmd.Example = "  td feature set sync_cli true\n  td feature set sync_autosync false\n  td feature set review_policy_mode trusted"

	rootCmd.AddCommand(featureCmd)
}

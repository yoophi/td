package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"text/tabwriter"

	"github.com/marcus/td/internal/workdir"
	"github.com/spf13/cobra"
)

// Keep the device-local persistence boundary injectable without changing HOME
// or redirecting the user's global settings during command contract tests.
var loadDirectoryAssociations = workdir.LoadAssociations
var saveDirectoryAssociations = workdir.SaveAssociations

func init() {
	configCmd.AddCommand(associateCmd)
	configCmd.AddCommand(associationsCmd)
	configCmd.AddCommand(dissociateCmd)
}

var associateCmd = &cobra.Command{
	Use:   "associate [dir] <target>",
	Short: "Associate a directory with a td project",
	Long: `Associate a directory with a td project path so td automatically
uses that project when run from the directory. This avoids needing
.td-root files in every repo.

If only one argument is given, it is treated as the target and the
current directory is used as the source.

Associations are stored in ~/.config/td/associations.json.`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		var dir, target string

		if len(args) == 2 {
			dir = args[0]
			target = args[1]
		} else {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("cannot determine working directory: %w", err)
			}
			dir = cwd
			target = args[0]
		}

		// Normalize to absolute paths
		var err error
		dir, err = filepath.Abs(dir)
		if err != nil {
			return fmt.Errorf("invalid directory path: %w", err)
		}
		dir = filepath.Clean(dir)

		target, err = filepath.Abs(target)
		if err != nil {
			return fmt.Errorf("invalid target path: %w", err)
		}
		target = filepath.Clean(target)

		// Validate target exists
		fi, err := os.Stat(target)
		if err != nil {
			return fmt.Errorf("target does not exist: %s", target)
		}
		if !fi.IsDir() {
			return fmt.Errorf("target is not a directory: %s", target)
		}

		// Load, update, save
		assoc, err := loadDirectoryAssociations()
		if err != nil {
			return fmt.Errorf("loading associations: %w", err)
		}

		assoc[dir] = target

		if err := saveDirectoryAssociations(assoc); err != nil {
			return fmt.Errorf("saving associations: %w", err)
		}
		if jsonMode(cmd) {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]string{"action": "associate", "directory": dir, "project": target})
		}

		fmt.Printf("Associated %s → %s\n", dir, target)
		return nil
	},
}

var associationsCmd = &cobra.Command{
	Use:     "associations",
	Aliases: []string{"assoc"},
	Short:   "List directory associations",
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		assoc, err := loadDirectoryAssociations()
		if err != nil {
			return fmt.Errorf("loading associations: %w", err)
		}
		if jsonMode(cmd) {
			entries := []map[string]string{}
			keys := make([]string, 0, len(assoc))
			for dir := range assoc {
				keys = append(keys, dir)
			}
			sort.Strings(keys)
			for _, dir := range keys {
				entries = append(entries, map[string]string{"directory": dir, "project": assoc[dir]})
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(entries)
		}

		if len(assoc) == 0 {
			fmt.Println("No directory associations configured.")
			fmt.Println("Use 'td config associate <target>' to create one.")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(w, "DIRECTORY\tPROJECT")

		// Sort for stable output
		keys := make([]string, 0, len(assoc))
		for k := range assoc {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		for _, k := range keys {
			_, _ = fmt.Fprintf(w, "%s\t%s\n", k, assoc[k])
		}
		return w.Flush()
	},
}

var dissociateCmd = &cobra.Command{
	Use:   "dissociate [dir]",
	Short: "Remove a directory association",
	Long: `Remove the association for a directory. If no directory is given,
the current directory is used.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		var dir string

		if len(args) == 1 {
			dir = args[0]
		} else {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("cannot determine working directory: %w", err)
			}
			dir = cwd
		}

		var err error
		dir, err = filepath.Abs(dir)
		if err != nil {
			return fmt.Errorf("invalid directory path: %w", err)
		}
		dir = filepath.Clean(dir)

		assoc, err := loadDirectoryAssociations()
		if err != nil {
			return fmt.Errorf("loading associations: %w", err)
		}

		if _, ok := assoc[dir]; !ok {
			return fmt.Errorf("no association found for %s", dir)
		}

		delete(assoc, dir)

		if err := saveDirectoryAssociations(assoc); err != nil {
			return fmt.Errorf("saving associations: %w", err)
		}
		if jsonMode(cmd) {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]string{"action": "dissociate", "directory": dir})
		}

		fmt.Printf("Removed association for %s\n", dir)
		return nil
	},
}

package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

func validateExportOptions(cmd *cobra.Command) error {
	format, _ := cmd.Flags().GetString("format")
	switch format {
	case "journal":
		return validateJournalExportFlags(cmd)
	case "json", "md":
	default:
		return fmt.Errorf("unsupported export format %q", format)
	}
	render, _ := cmd.Flags().GetBool("render-markdown")
	if render && format != "md" {
		return fmt.Errorf("--render-markdown requires --format md")
	}
	return nil
}

// Both stores serialize completely before reaching this common output boundary.
func writeExportOutput(cmd *cobra.Command, path string, data []byte) error {
	format, _ := cmd.Flags().GetString("format")
	if path == "" {
		if jsonMode(cmd) && format == "md" {
			rendered, _ := cmd.Flags().GetBool("render-markdown")
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{
				"format": format, "document": string(data), "rendered": rendered,
			})
		}
		_, err := fmt.Fprintln(cmd.OutOrStdout(), string(data))
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".td-export-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if _, err = f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	if jsonMode(cmd) {
		err = json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{
			"action": "exported", "format": format, "path": path,
		})
	} else {
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Exported to %s\n", path)
	}
	if err != nil {
		return fmt.Errorf("export saved to %s, but receipt output failed: %w", path, err)
	}
	return nil
}

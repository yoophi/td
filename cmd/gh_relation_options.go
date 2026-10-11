package cmd

import (
	"slices"
	"strings"

	"github.com/spf13/cobra"
)

type gitHubRelationOptions struct{ depends, blocks *[]string }

func (o gitHubRelationOptions) changed() bool { return o.depends != nil || o.blocks != nil }
func readGitHubRelationOptions(cmd *cobra.Command) (gitHubRelationOptions, error) {
	options := gitHubRelationOptions{}
	for _, name := range []string{"depends-on", "blocks"} {
		if !cmd.Flags().Changed(name) {
			continue
		}
		values, err := cmd.Flags().GetStringArray(name)
		if err != nil {
			return options, err
		}
		ids := []string{}
		for _, value := range mergeMultiValueFlag(values) {
			if strings.TrimSpace(value) == "" {
				continue
			}
			id, err := canonicalGitHubID(value)
			if err != nil {
				return options, err
			}
			if !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
		if name == "depends-on" {
			options.depends = &ids
		} else {
			options.blocks = &ids
		}
	}
	return options, nil
}

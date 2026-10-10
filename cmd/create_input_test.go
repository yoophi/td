package cmd

import (
	"strings"
	"testing"
)

func TestCreateInputResolution(t *testing.T) {
	for _, tc := range []struct {
		name                                            string
		args                                            []string
		titleFlag, typeFlag, fixed, title, typ, wantErr string
	}{
		{name: "positional", args: []string{"Fixture title"}, title: "Fixture title"},
		{name: "title flag", titleFlag: "Fixture title", title: "Fixture title"},
		{name: "positional type", args: []string{"task", "Fixture title"}, title: "Fixture title", typ: "task"},
		{name: "matching type", args: []string{"task", "Fixture title"}, typeFlag: "task", title: "Fixture title", typ: "task"},
		{name: "fixed title flag", titleFlag: "Fixture title", fixed: "task", title: "Fixture title", typ: "task"},
		{name: "two titles", args: []string{"Positional title"}, titleFlag: "Flag title", wantErr: "do not combine"},
		{name: "unused words", args: []string{"Fixture", "title"}, wantErr: "quote"},
		{name: "unused prefix words", args: []string{"task", "Fixture", "title"}, wantErr: "quote"},
		{name: "conflicting type", args: []string{"task", "Fixture title"}, typeFlag: "bug", wantErr: "conflicts"},
		{name: "fixed conflicting type", args: []string{"bug", "Fixture title"}, fixed: "task", wantErr: "conflicts"},
		{name: "missing title", wantErr: "title is required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := shortcutTestCommand(createCmd, true)
			if tc.titleFlag != "" {
				if err := cmd.Flags().Set("title", tc.titleFlag); err != nil {
					t.Fatal(err)
				}
			}
			if tc.typeFlag != "" {
				if err := cmd.Flags().Set("type", tc.typeFlag); err != nil {
					t.Fatal(err)
				}
			}
			title, typ, err := resolveCreateInput(cmd, tc.args, tc.fixed)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatal(title, typ, err)
				}
			} else if err != nil || title != tc.title || typ != tc.typ {
				t.Fatal(title, typ, err)
			}
		})
	}
}

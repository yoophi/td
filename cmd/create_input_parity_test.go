package cmd

import (
	"encoding/json"
	"testing"

	"github.com/marcus/td/internal/models"
)

func TestCreateInputSQLiteStoredAndGitHubModelParity(t *testing.T) {
	database, _ := setupReadJSONTest(t)
	for _, tc := range []struct {
		name                string
		args                []string
		titleFlag, typeFlag string
	}{
		{name: "uppercase positional type", args: []string{"TASK", "Input parity fixture title"}},
		{name: "positional alias overrides title prefix", args: []string{"story", "bug: Input parity fixture title"}},
		{name: "explicit type", args: []string{"feature", "task: Input parity fixture title"}, typeFlag: "feature"},
		{name: "title flag", titleFlag: "Input parity fixture title"},
		{name: "title prefix inference", args: []string{"bug: Input parity fixture title"}},
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
			github, err := newGitHubIssue(cmd, tc.args)
			if err != nil {
				t.Fatal(err)
			}
			var runErr error
			out := captureStdout(t, func() {
				_, runErr = executeGitHubTest(cmd, append(tc.args, "--json")...)
			})
			var receipt models.Issue
			if runErr != nil || json.Unmarshal([]byte(out), &receipt) != nil {
				t.Fatal(out, runErr)
			}
			stored, err := database.GetIssue(receipt.ID)
			if err != nil || stored.Title != github.Title || stored.Type != github.Type {
				t.Fatal("SQL stored fields differ from GitHub creation model", stored, github, err)
			}
		})
	}
}

func TestGitHubCreatePositionalTypeStoredFields(t *testing.T) {
	githubScheduleTestDir(t)
	for _, tc := range []struct {
		args  []string
		title string
		typ   models.Type
	}{
		{[]string{"TASK", "Stored positional type fixture"}, "Stored positional type fixture", models.TypeTask},
		{[]string{"story", "bug: Stored positional type fixture"}, "bug: Stored positional type fixture", models.TypeFeature},
	} {
		cmd := shortcutTestCommand(createCmd, true)
		out, err := executeGitHubTest(cmd, append(tc.args, "--json")...)
		var receipt models.Issue
		if err != nil || json.Unmarshal([]byte(out), &receipt) != nil {
			t.Fatal(out, err)
		}
		out, err = executeGitHubTest(githubTestCommand(showCmd), receipt.ID, "--json")
		var stored models.Issue
		if err != nil || json.Unmarshal([]byte(out), &stored) != nil || stored.Title != tc.title || stored.Type != tc.typ {
			t.Fatal("persisted GH issue fields differ", out, err)
		}
	}
}

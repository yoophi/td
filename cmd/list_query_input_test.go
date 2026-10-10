package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/marcus/td/internal/models"
	"github.com/spf13/cobra"
)

func TestListQueryRejectsIgnoredSelectionFlagsBeforeExecution(t *testing.T) {
	for _, name := range []string{"id", "status", "type", "labels", "priority", "points", "search", "implementer", "reviewer", "reviewable", "include-approved", "parent", "epic", "mine", "open", "created", "updated", "closed", "all", "deferred", "overdue", "surfacing", "due-soon"} {
		cmd := shortcutTestCommand(listCmd, false)
		ran := false
		cmd.RunE = func(*cobra.Command, []string) error { ran = true; return nil }
		value := "fixture"
		if cmd.Flags().Lookup(name).Value.Type() == "bool" {
			value = "false" // Even an explicitly false ignored flag must be rejected.
		}
		cmd.SetArgs([]string{"--filter", "type = task", "--" + name + "=" + value})
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "cannot be combined") || ran {
			t.Fatal(name, err, ran)
		}
	}
}

func TestListQueryFixedTypesOnSQLite(t *testing.T) {
	database, _ := setupReadJSONTest(t)
	for _, typ := range []models.Type{models.TypeTask, models.TypeEpic, models.TypeBug} {
		if err := database.CreateIssue(&models.Issue{Title: "Query shortcut fixture", Type: typ, Status: models.StatusOpen, Priority: models.PriorityP1}); err != nil {
			t.Fatal(err)
		}
	}
	for _, original := range []*cobra.Command{taskListCmd, epicListCmd} {
		cmd := shortcutTestCommand(original, false)
		var runErr error
		out := captureStdout(t, func() {
			_, runErr = executeGitHubTest(cmd, "--filter", "status = open", "--json")
		})
		var rows []models.Issue
		if runErr != nil || json.Unmarshal([]byte(out), &rows) != nil || len(rows) != 1 {
			t.Fatal(original.Name(), rows, runErr, out)
		}
		want := models.TypeTask
		if original == epicListCmd {
			want = models.TypeEpic
		}
		if rows[0].Type != want {
			t.Fatal("shortcut TDQ lost its fixed type", rows[0])
		}
	}
}

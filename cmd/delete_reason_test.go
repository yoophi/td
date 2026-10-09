package cmd

import (
	"github.com/marcus/td/internal/models"
	"strings"
	"testing"
)

func TestSQLiteDeleteRestoreReasonsAndBlankValidation(t *testing.T) {
	database, actor := setupReadJSONTest(t)
	issue := &models.Issue{Title: "Fixture reason", Type: models.TypeTask, Priority: models.PriorityP2}
	if err := database.CreateIssue(issue); err != nil {
		t.Fatal(err)
	}
	if _, err := executeGitHubTest(deletionTestCommand(deleteCmd), issue.ID, "--reason", "   "); err == nil {
		t.Fatal("blank reason accepted")
	}
	current, err := database.GetIssue(issue.ID)
	if err != nil || current.DeletedAt != nil {
		t.Fatal("blank reason changed visibility")
	}
	if _, err := executeGitHubTest(deletionTestCommand(deleteCmd), issue.ID, "--reason", " Delete fixture 理由 \n説明 "); err != nil {
		t.Fatal(err)
	}
	last, err := database.GetLastAction(actor)
	if err != nil || last == nil || last.ActionType != models.ActionDelete {
		t.Fatalf("reason log obscured delete undo action: %+v %v", last, err)
	}
	if _, err := executeGitHubTest(deletionTestCommand(restoreCmd), issue.ID, "--reason", " Restore fixture reason "); err != nil {
		t.Fatal(err)
	}
	last, err = database.GetLastAction(actor)
	if err != nil || last == nil || last.ActionType != models.ActionRestore {
		t.Fatalf("reason log obscured restore undo action: %+v %v", last, err)
	}
	logs, err := database.GetLogs(issue.ID, 0)
	if err != nil || len(logs) != 2 {
		t.Fatalf("reason logs: %v %v", logs, err)
	}
	found := map[string]bool{}
	for _, log := range logs {
		found[log.Message] = true
	}
	if !found["Deleted: Delete fixture 理由 \n説明"] || !found["Restored: Restore fixture reason"] {
		t.Fatalf("reason formatting lost: %v", logs)
	}
	current, err = database.GetIssue(issue.ID)
	if err != nil || current.DeletedAt != nil {
		t.Fatal("restore failed")
	}
	for _, log := range logs {
		if strings.TrimSpace(log.SessionID) == "" {
			t.Fatal("reason actor missing")
		}
	}
}

func TestSQLiteDeletionReasonFailureReportsSavedVisibility(t *testing.T) {
	for _, restore := range []bool{false, true} {
		t.Run(map[bool]string{false: "delete", true: "restore"}[restore], func(t *testing.T) {
			database, _ := setupReadJSONTest(t)
			issue := &models.Issue{Title: "Fixture partial logging", Type: models.TypeTask, Priority: models.PriorityP2}
			if err := database.CreateIssue(issue); err != nil {
				t.Fatal(err)
			}
			if restore {
				if err := database.DeleteIssueLogged(issue.ID, "fixture-actor"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := database.Conn().Exec(`CREATE TRIGGER fail_reason_log BEFORE INSERT ON logs BEGIN SELECT RAISE(ABORT,'fixture log denied'); END;`); err != nil {
				t.Fatal(err)
			}
			command := deleteCmd
			if restore {
				command = restoreCmd
			}
			_, err := executeGitHubTest(deletionTestCommand(command), issue.ID, "--reason", "Fixture explicit reason")
			if err == nil || !strings.Contains(err.Error(), "reason logging failed") || !strings.Contains(err.Error(), "inspect current state before retrying") {
				t.Fatalf("partial failure hidden: %v", err)
			}
			current, err := database.GetIssue(issue.ID)
			if err != nil || (current.DeletedAt == nil) != restore {
				t.Fatalf("saved visibility mismatch: %+v %v", current, err)
			}
		})
	}
}

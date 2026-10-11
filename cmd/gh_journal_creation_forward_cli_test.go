package cmd

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/marcus/td/internal/ghstore"
)

func TestGitHubJournalCLICreationCommandForwardOwnership(t *testing.T) {
	for _, blocks := range []bool{false, true} {
		t.Run(map[bool]string{false: "dependencies-only", true: "both-directions"}[blocks], func(t *testing.T) {
			run, execute := workflowJournalCLI(t)
			for _, title := range []string{"Dependency", "Reverse target", "Parent"} {
				execute("create", title, "--minor")
			}
			args := []string{"create", "Complete related creation", "--depends-on", "gh-6", "--parent", "gh-8", "--minor"}
			if blocks {
				args = append(args, "--blocks", "gh-7")
			}
			out := execute(args...)
			var result struct {
				Issue ghstore.Record `json:"issue"`
			}
			if err := json.Unmarshal([]byte(out), &result); err != nil || result.Issue.ID != "gh-9" || result.Issue.Details == nil || !slices.Equal(result.Issue.Details.Dependencies, []string{"gh-6"}) || result.Issue.ParentID != "gh-8" || len(result.Issue.Details.Transitions) != 0 {
				t.Fatal("creation did not POST its complete input", out, err)
			}
			var rows []struct {
				ID              string
				Phase           string
				CreationCommand *ghstore.ActionCreationCommand `json:"creation_command"`
			}
			if out := execute("last", "--n", "1"); json.Unmarshal([]byte(out), &rows) != nil || len(rows) != 1 || rows[0].Phase != "applied" || rows[0].CreationCommand == nil {
				t.Fatal("missing creation command history", out)
			}
			command := rows[0].CreationCommand
			if command.Allocation.Proof != "verified" || command.Forward.Proof != "verified" || command.Partial || (command.Relations != nil) != blocks {
				t.Fatal("incorrect forward proof", command)
			}
			if blocks {
				child := command.Relations
				if child.CreationOwner == "" || child.OperationID != command.RelationOperationID || child.Group == nil || child.Group.Command != "update-relations" {
					t.Fatal("child lost creation owner")
				}
				before, journals := relationFixtureBytes(t, "TD_SCHEDULE_STATE"), relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")
				out, stderr, err := run("undo", "--reconcile", child.ID, "--apply", "--json")
				if err == nil || !strings.Contains(out+stderr, "complete creation command") || !bytes.Equal(before, relationFixtureBytes(t, "TD_SCHEDULE_STATE")) || !bytes.Equal(journals, relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")) {
					t.Fatal("child recovery escaped parent command", out, stderr, err)
				}
			}
		})
	}
}

func TestGitHubJournalCLICreationIntentFailureRetainsAllocation(t *testing.T) {
	for _, mode := range []string{"intent-denied", "receipt-denied", "receipt-lost"} {
		t.Run(mode, func(t *testing.T) {
			run, execute := workflowJournalCLI(t)
			execute("create", "Original dependency", "--minor")
			before := relationFixtureBytes(t, "TD_SCHEDULE_STATE")
			t.Setenv("TD_GH_JOURNAL_FAIL", mode)
			out, stderr, err := run("create", "Uncertain related creation", "--depends-on", "gh-6", "--minor", "--json")
			if err == nil || !strings.Contains(out+stderr, "creation command") {
				t.Fatal("uncertain command reported success", out, stderr, err)
			}
			t.Setenv("TD_GH_JOURNAL_FAIL", "")
			if mode == "intent-denied" {
				if !bytes.Equal(before, relationFixtureBytes(t, "TD_SCHEDULE_STATE")) {
					t.Fatal("POST ran without durable complete intent")
				}
				return
			}
			if !strings.Contains(out+stderr, "gh-7") {
				t.Fatal("known allocation omitted from partial failure", out, stderr)
			}
			var actual ghstore.Record
			if out := execute("show", "gh-7"); json.Unmarshal([]byte(out), &actual) != nil || actual.Details == nil || !slices.Equal(actual.Details.Dependencies, []string{"gh-6"}) {
				t.Fatal("lost response discarded actual complete POST result", out)
			}
			var rows []struct {
				Phase           string
				CreationCommand *ghstore.ActionCreationCommand `json:"creation_command"`
			}
			if out := execute("last", "--n", "1"); json.Unmarshal([]byte(out), &rows) != nil || len(rows) != 1 || rows[0].Phase != "prepared" || rows[0].CreationCommand == nil || (rows[0].CreationCommand.Allocation != nil) != (mode == "receipt-lost") {
				t.Fatal("uncertain receipt acquired verified proof", out)
			}
			var history []struct{ ID string }
			if out := execute("last", "--n", "1"); json.Unmarshal([]byte(out), &history) != nil || len(history) != 1 {
				t.Fatal(out)
			}
			state, journals := relationFixtureBytes(t, "TD_SCHEDULE_STATE"), relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")
			execute("undo", "--reconcile", history[0].ID)
			if !bytes.Equal(state, relationFixtureBytes(t, "TD_SCHEDULE_STATE")) || !bytes.Equal(journals, relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")) {
				t.Fatal("creation recovery preview wrote")
			}
			var recovery ghstore.ActionReconcileResult
			if out := execute("undo", "--reconcile", history[0].ID, "--apply"); json.Unmarshal([]byte(out), &recovery) != nil || recovery.Journal == nil || recovery.Journal.Phase != "applied" || recovery.ForwardOutcome == nil || recovery.ForwardOutcome.TargetWriteVerified || recovery.ForwardOutcome.CausalUndoChainEligible || recovery.TargetWriteReplayed {
				t.Fatal("creation recovery misrepresented observed proof", out)
			}
			allocationProof := "observed"
			if mode == "receipt-lost" {
				allocationProof = "verified"
			}
			if recovery.Journal.CreationCommand.Allocation.Proof != allocationProof || recovery.Journal.CreationCommand.Forward.Proof != "observed" || recovery.Journal.CreationCommand.Partial {
				t.Fatal("allocation provenance was replaced", recovery.Journal)
			}
			if !bytes.Equal(state, relationFixtureBytes(t, "TD_SCHEDULE_STATE")) {
				t.Fatal("creation recovery replayed target writes")
			}
			journals = relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")
			execute("undo", "--reconcile", history[0].ID, "--apply")
			if !bytes.Equal(state, relationFixtureBytes(t, "TD_SCHEDULE_STATE")) || !bytes.Equal(journals, relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")) {
				t.Fatal("completed creation confirmation wrote")
			}
		})
	}
}

func TestGitHubJournalCLICreationRecoveryDoesNotReplayMissingRelations(t *testing.T) {
	run, execute := workflowJournalCLI(t)
	execute("create", "Original dependency", "--minor")
	execute("create", "Original reverse target", "--minor")
	t.Setenv("TD_GH_JOURNAL_FAIL", "receipt-denied")
	out, stderr, err := run("create", "Partial creation", "--depends-on", "gh-6", "--blocks", "gh-7", "--minor", "--json")
	if err == nil || !strings.Contains(out+stderr, "gh-8") {
		t.Fatal("missing partial allocation guidance", out, stderr, err)
	}
	t.Setenv("TD_GH_JOURNAL_FAIL", "")
	var rows []struct{ ID string }
	if out := execute("last", "--n", "1"); json.Unmarshal([]byte(out), &rows) != nil || len(rows) != 1 {
		t.Fatal(out)
	}
	state := relationFixtureBytes(t, "TD_SCHEDULE_STATE")
	var recovered ghstore.ActionReconcileResult
	if out := execute("undo", "--reconcile", rows[0].ID, "--apply"); json.Unmarshal([]byte(out), &recovered) != nil || recovered.Journal == nil || !recovered.Partial || !recovered.Journal.CreationCommand.Partial || recovered.Journal.CreationCommand.Relations != nil || recovered.TargetWriteReplayed {
		t.Fatal("missing relationship was replayed or hidden", out)
	}
	if !bytes.Equal(state, relationFixtureBytes(t, "TD_SCHEDULE_STATE")) {
		t.Fatal("partial recovery changed a target")
	}
	if out, stderr, err := run("last", "--n", "1"); err != nil || !strings.Contains(out, "partial creation") {
		t.Fatal("human history hides partial creation", out, stderr, err)
	}
	if out, stderr, err := run("undo", "--reconcile", rows[0].ID); err != nil || !strings.Contains(out, "partial=true") {
		t.Fatal("human recovery hides partial creation", out, stderr, err)
	}
	journals := relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")
	execute("undo", "--reconcile", rows[0].ID, "--apply")
	if !bytes.Equal(state, relationFixtureBytes(t, "TD_SCHEDULE_STATE")) || !bytes.Equal(journals, relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")) {
		t.Fatal("completed partial confirmation wrote")
	}
}

func TestGitHubJournalCLICreationRecoveryOwnsExistingRelationEffects(t *testing.T) {
	for _, failure := range []string{"member-receipt-denied", "relation-receipt-denied", "command-receipt-denied"} {
		t.Run(failure, func(t *testing.T) {
			run, execute := workflowJournalCLI(t)
			execute("create", "Original dependency", "--minor")
			execute("create", "Original reverse target", "--minor")
			t.Setenv("TD_GH_CREATION_FAIL", failure)
			out, stderr, err := run("create", "Partial creation receipts", "--depends-on", "gh-6", "--blocks", "gh-7", "--minor", "--json")
			if err == nil || !strings.Contains(out+stderr, "gh-8") || !strings.Contains(out+stderr, "creation receipt denied") {
				t.Fatal("failure not injected after actual effects", out, stderr, err)
			}
			t.Setenv("TD_GH_CREATION_FAIL", "")
			var reverse ghstore.Record
			if out := execute("show", "gh-7"); json.Unmarshal([]byte(out), &reverse) != nil || reverse.Details == nil || !slices.Equal(reverse.Details.Dependencies, []string{"gh-8"}) {
				t.Fatal("actual relation missing before receipt recovery", out)
			}
			var rows []struct {
				ID    string
				Phase string
			}
			if out := execute("last", "--n", "1"); json.Unmarshal([]byte(out), &rows) != nil || len(rows) != 1 || rows[0].Phase != "prepared" {
				t.Fatal("partial child escaped creation history", out)
			}
			state, journals := relationFixtureBytes(t, "TD_SCHEDULE_STATE"), relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")
			execute("undo", "--reconcile", rows[0].ID)
			if !bytes.Equal(state, relationFixtureBytes(t, "TD_SCHEDULE_STATE")) || !bytes.Equal(journals, relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")) {
				t.Fatal("partial child preview wrote")
			}
			var recovered ghstore.ActionReconcileResult
			if out := execute("undo", "--reconcile", rows[0].ID, "--apply"); json.Unmarshal([]byte(out), &recovered) != nil || recovered.Journal == nil || recovered.Journal.Phase != "applied" || recovered.Partial || recovered.Journal.CreationCommand.Relations == nil || recovered.ForwardOutcome.TargetWriteVerified || recovered.TargetWriteReplayed {
				t.Fatal("child recovery lost aggregate or promoted proof", out)
			}
			if !bytes.Equal(state, relationFixtureBytes(t, "TD_SCHEDULE_STATE")) {
				t.Fatal("child recovery repeated a target update")
			}
			journals = relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")
			execute("undo", "--reconcile", rows[0].ID, "--apply")
			if !bytes.Equal(state, relationFixtureBytes(t, "TD_SCHEDULE_STATE")) || !bytes.Equal(journals, relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")) {
				t.Fatal("completed child confirmation wrote")
			}
		})
	}
}

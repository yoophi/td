package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/marcus/td/internal/ghstore"
)

func TestGitHubJournalCLICreationInverseRecovery(t *testing.T) {
	for _, mode := range []string{"child-receipt", "deletion-response", "parent-receipt", "partial-forward"} {
		t.Run(mode, func(t *testing.T) {
			run, execute := workflowJournalCLI(t)
			execute("create", "Original dependency", "--minor")
			execute("create", "Original reverse target", "--minor")
			args := []string{"create", "Related creation", "--depends-on", "gh-6", "--minor"}
			if mode != "deletion-response" {
				args = append(args, "--blocks", "gh-7")
			}
			if mode == "partial-forward" {
				t.Setenv("TD_GH_JOURNAL_FAIL", "receipt-denied")
				if _, _, err := run(append(args, "--json")...); err == nil {
					t.Fatal("expected partial creation")
				}
				t.Setenv("TD_GH_JOURNAL_FAIL", "")
			} else {
				execute(args...)
			}
			var rows []struct{ ID string }
			if out := execute("last", "--n", "1"); json.Unmarshal([]byte(out), &rows) != nil || len(rows) != 1 {
				t.Fatal(out)
			}
			id := rows[0].ID
			switch mode {
			case "child-receipt":
				t.Setenv("TD_GH_JOURNAL_FAIL", "undo-completion-denied")
			case "deletion-response":
				t.Setenv("TD_GH_JOURNAL_FAIL", "target-lost")
			case "parent-receipt":
				t.Setenv("TD_GH_CREATION_FAIL", "inverse-command-receipt-denied")
			case "partial-forward":
				execute("undo", "--reconcile", id, "--apply")
			}
			if mode == "partial-forward" {
				execute("undo")
			} else {
				if out, stderr, err := run("undo", "--json"); err == nil || !strings.Contains(out+stderr, "creation") {
					t.Fatal("failed creation inverse reported success", out, stderr, err)
				}
			}
			t.Setenv("TD_GH_JOURNAL_FAIL", "")
			t.Setenv("TD_GH_CREATION_FAIL", "")
			state, journals := relationFixtureBytes(t, "TD_SCHEDULE_STATE"), relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")
			execute("undo", "--reconcile", id)
			if !bytes.Equal(state, relationFixtureBytes(t, "TD_SCHEDULE_STATE")) || !bytes.Equal(journals, relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")) {
				t.Fatal("inverse recovery preview wrote")
			}
			var recovered ghstore.ActionReconcileResult
			if out := execute("undo", "--reconcile", id, "--apply"); json.Unmarshal([]byte(out), &recovered) != nil || recovered.TargetWriteReplayed || recovered.Journal == nil {
				t.Fatal("inverse recovery lost result", out)
			}
			if !bytes.Equal(state, relationFixtureBytes(t, "TD_SCHEDULE_STATE")) {
				t.Fatal("inverse recovery replayed a target write")
			}
			if mode == "child-receipt" {
				if recovered.Journal.Phase != "undo_prepared" || !recovered.Partial {
					t.Fatal("unfinished creation inverse claimed completion", recovered)
				}
				execute("undo")
			} else if recovered.Journal.Phase != "undone" {
				t.Fatal("creation inverse was not completed", recovered)
			}
			if mode == "deletion-response" || mode == "parent-receipt" {
				if recovered.UndoOutcome == nil || recovered.UndoOutcome.TargetWriteVerified || recovered.UndoOutcome.CausalUndoChainEligible {
					t.Fatal("observed inverse acquired verified provenance", recovered)
				}
			}
			state, journals = relationFixtureBytes(t, "TD_SCHEDULE_STATE"), relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")
			execute("undo", "--reconcile", id, "--apply")
			if !bytes.Equal(state, relationFixtureBytes(t, "TD_SCHEDULE_STATE")) || !bytes.Equal(journals, relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")) {
				t.Fatal("completed creation inverse confirmation wrote")
			}
		})
	}
}

func TestGitHubJournalCLICreationInverseRejectsForeignChanges(t *testing.T) {
	for _, mode := range []string{"source", "dependency", "outside-dependent", "new-child", "native-comment", "native-event", "after-completion", "after-edge-restoration"} {
		t.Run(mode, func(t *testing.T) {
			run, execute := workflowJournalCLI(t)
			execute("create", "Original dependency", "--minor")
			execute("create", "Original reverse target", "--minor")
			execute("create", "Related creation", "--depends-on", "gh-6", "--blocks", "gh-7", "--minor")
			var rows []struct{ ID string }
			if out := execute("last", "--n", "1"); json.Unmarshal([]byte(out), &rows) != nil || len(rows) != 1 {
				t.Fatal(out)
			}
			if mode == "after-completion" || mode == "after-edge-restoration" {
				execute("undo")
			}
			var fixture struct {
				Issues   map[string]map[string]any `json:"issues"`
				Comments []map[string]any          `json:"comments"`
			}
			if err := json.Unmarshal(relationFixtureBytes(t, "TD_SCHEDULE_STATE"), &fixture); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "source", "after-completion":
				fixture.Issues["8"]["title"] = "Foreign source edit"
			case "after-edge-restoration":
				fixture.Issues["7"]["title"] = "Foreign restored-edge edit"
			case "dependency":
				fixture.Issues["6"]["title"] = "Foreign dependency edit"
			case "outside-dependent", "new-child":
				body := fixture.Issues["1"]["body"].(string)
				start := strings.Index(body, "<!-- td:issue:v1\n") + len("<!-- td:issue:v1\n")
				end := strings.LastIndex(body, "\n-->")
				var metadata map[string]any
				if err := json.Unmarshal([]byte(body[start:end]), &metadata); err != nil {
					t.Fatal(err)
				}
				details, _ := metadata["details"].(map[string]any)
				if details == nil {
					details = map[string]any{}
					metadata["details"] = details
				}
				if mode == "new-child" {
					details["parent_id"] = "gh-8"
				} else {
					details["dependencies"] = []string{"gh-8"}
				}
				encoded, _ := json.Marshal(metadata)
				fixture.Issues["1"]["body"] = body[:start] + string(encoded) + body[end:]
			case "native-event":
				var journal map[string]any
				if err := json.Unmarshal(relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL"), &journal); err != nil {
					t.Fatal(err)
				}
				journal["native_events"].(map[string]any)["8"] = []map[string]any{{"id": 1, "event": "closed"}, {"id": 2, "event": "reopened"}}
				data, _ := json.Marshal(journal)
				if err := os.WriteFile(os.Getenv("TD_GH_FIXTURE_JOURNAL"), data, 0600); err != nil {
					t.Fatal(err)
				}
			case "native-comment":
				fixture.Comments = append(fixture.Comments, map[string]any{"id": 12345, "body": "Foreign activity", "issue_url": "https://api.github.com/repos/owner/repo/issues/8", "created_at": "2026-10-10T00:00:00Z", "updated_at": "2026-10-10T00:00:00Z", "user": map[string]any{"login": "someone-else"}})
			}
			encoded, _ := json.Marshal(fixture)
			if err := os.WriteFile(os.Getenv("TD_SCHEDULE_STATE"), encoded, 0600); err != nil {
				t.Fatal(err)
			}
			state, journals := relationFixtureBytes(t, "TD_SCHEDULE_STATE"), relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")
			args := []string{"undo", "--json"}
			if mode == "after-completion" || mode == "after-edge-restoration" {
				args = append(args, "--reconcile", rows[0].ID, "--apply")
			}
			if out, stderr, err := run(args...); err == nil {
				t.Fatal("foreign edit was accepted", out, stderr)
			}
			if !bytes.Equal(state, relationFixtureBytes(t, "TD_SCHEDULE_STATE")) || !bytes.Equal(journals, relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")) {
				t.Fatal("creation inverse wrote after foreign change")
			}
		})
	}
}

package ghstore

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func importCommandFixture(t *testing.T) (ActionJournal, []ImportItem, *ImportPlan) {
	t.Helper()
	c, native, _, comments := importBatchFixture(t)
	clock := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	for _, issue := range native {
		issue["created_at"], issue["updated_at"] = clock, clock
		issue["user"] = map[string]string{"login": "original-author"}
	}
	comment := apiComment{ID: 7, Body: "Existing native activity", IssueURL: "https://api.github.com/repos/owner/repo/issues/1", CreatedAt: clock, UpdatedAt: clock}
	comment.User.Login = "original-author"
	*comments = append(*comments, comment)
	exported, err := c.ExportIssues(context.Background(), true, true)
	if err != nil {
		t.Fatal(err)
	}
	var existing ImportItem
	for _, e := range exported {
		if e.Record.ID == "gh-1" {
			existing = ImportItem{Issue: e.Record.Issue, GitHub: &e}
		}
	}
	fresh := importTestItem("td-new")
	fresh.SourceNamespace = "legacy-source"
	items := []ImportItem{existing, fresh}
	plan, err := c.PlanImport(context.Background(), items, true)
	if err != nil {
		t.Fatal(err)
	}
	input, err := prepareImportCommand(items, true, plan, map[string][]nativeStateEvent{"gh-1": {}})
	if err != nil {
		t.Fatal(err)
	}
	return ActionJournal{OperationID: "td-op-import", SessionID: "ses_import", CarrierID: "gh-999", Action: "import", Phase: "prepared", ImportCommand: input}, items, plan
}

func TestImportCommandIntentPreservesOriginalNativeAndPortableInputs(t *testing.T) {
	a, items, plan := importCommandFixture(t)
	if err := a.validate(); err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(a)
	items[0].GitHub.Activity[0].Message = "Changed after preparation"
	items[1].Issue.Title = "Changed request"
	for n := range plan.snapshot.entries {
		entry := &plan.snapshot.entries[n]
		if entry.Issue.Number == 1 {
			entry.Issue.Title = "Changed observation"
			entry.Comments[0].Body = "Changed original comment"
		}
	}
	after, _ := json.Marshal(a)
	if string(before) != string(after) {
		t.Fatal("command retained caller/planner aliases")
	}
	if a.ImportCommand.Members[0].Before.Issue.Number != 1 || a.ImportCommand.Members[1].TargetID != "" || a.ImportCommand.Members[1].Before != nil {
		t.Fatal("original native identity was lost or a future allocation was invented")
	}
	comment := journalCommentFixture(t, a)
	comment.IssueURL = "https://api.github.com/repos/owner/repo/issues/999"
	decoded, err := decodeActionJournal(comment, "gh-999")
	if err != nil || !reflect.DeepEqual(decoded.ActionJournal, a) {
		t.Fatal("import intent did not round-trip", err)
	}
	comment.Body = strings.Replace(comment.Body, actionImportCommandStart, actionJournalStart, 1)
	if _, err := decodeActionJournal(comment, "gh-999"); err == nil {
		t.Fatal("legacy version accepted import payload")
	}
}

func TestImportCommandIntentRejectsChangedMembershipAndForeignScope(t *testing.T) {
	a, _, _ := importCommandFixture(t)
	for _, tc := range []struct {
		name   string
		change func(*ActionJournal)
	}{
		{"missing source", func(a *ActionJournal) { a.ImportCommand.Members = a.ImportCommand.Members[:1] }},
		{"changed digest", func(a *ActionJournal) { a.ImportCommand.Members[0].Digest = strings.Repeat("f", 64) }},
		{"changed identity", func(a *ActionJournal) { a.ImportCommand.Members[0].SourceKey = "github:other/repo:gh-1" }},
		{"changed force", func(a *ActionJournal) { a.ImportCommand.Force = false }},
		{"overwrite disguised as skip", func(a *ActionJournal) { a.ImportCommand.Members[0].Action = "skip" }},
		{"existing target disguised as create", func(a *ActionJournal) {
			a.ImportCommand.Members[0].Action = "create"
			a.ImportCommand.Members[0].TargetID = ""
			a.ImportCommand.Members[0].Before = nil
		}},
		{"guessed allocation", func(a *ActionJournal) { a.ImportCommand.Members[1].TargetID = "gh-4" }},
		{"missing original", func(a *ActionJournal) { a.ImportCommand.OriginalTargets = []ImportTargetIdentity{} }},
		{"duplicate original", func(a *ActionJournal) {
			a.ImportCommand.OriginalTargets = []ImportTargetIdentity{{IssueID: "gh-1"}, {IssueID: "gh-1"}}
		}},
		{"unobserved recovered allocation", func(a *ActionJournal) {
			a.ImportCommand.OriginalTargets[1].OperationID = a.ImportCommand.Members[1].OperationID
		}},
		{"duplicate allocation identity", func(a *ActionJournal) {
			a.ImportCommand.OriginalTargets[0].OperationID = a.ImportCommand.Members[1].OperationID
			a.ImportCommand.OriginalTargets[1].OperationID = a.ImportCommand.Members[1].OperationID
		}},
		{"changed native allocation identity", func(a *ActionJournal) {
			a.ImportCommand.OriginalTargets[0].OperationID = "td-import-v1-changed"
		}},
		{"omitted observation", func(a *ActionJournal) { a.ImportCommand.Members[0].Before = nil }},
		{"omitted event evidence", func(a *ActionJournal) { a.ImportCommand.Members[0].Before.Events = nil }},
		{"foreign native comment", func(a *ActionJournal) {
			a.ImportCommand.Members[0].Before.Comments[0].IssueURL = "https://api.github.com/repos/other/repo/issues/1"
		}},
		{"unknown source fields", func(a *ActionJournal) {
			a.ImportCommand.Input = strings.Replace(a.ImportCommand.Input, `"issue":`, `"unknown":true,"issue":`, 1)
		}},
		{"unverified completion", func(a *ActionJournal) { a.Phase = "applied" }},
		{"foreign command", func(a *ActionJournal) { a.Action = "create" }},
		{"foreign snapshots", func(a *ActionJournal) { s := journalStateFixture(t, "Injected"); a.After = &s }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copy, err := copyCreationJournal(a)
			if err != nil {
				t.Fatal(err)
			}
			tc.change(&copy)
			if err := copy.validate(); err == nil {
				t.Fatal("accepted invalid import command")
			}
		})
	}
}

func TestImportCommandIntentUsesExistingBoundedCompressedTransport(t *testing.T) {
	a, _, _ := importCommandFixture(t)
	items, err := a.ImportCommand.inputItems()
	if err != nil {
		t.Fatal(err)
	}
	items[1].Issue.Description = strings.Repeat("Portable description before native allocation. ", 2500)
	data, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	a.ImportCommand.Input = string(data)
	a.ImportCommand.Members[1].Digest, err = items[1].Digest()
	if err != nil {
		t.Fatal(err)
	}
	body, err := renderWriteActionJournal(a)
	if err != nil || !strings.Contains(body, actionCompressedJournalStart) {
		t.Fatal("import did not use bounded transport", err)
	}
	comment := journalCommentFixture(t, a)
	comment.Body, comment.IssueURL = body, "https://api.github.com/repos/owner/repo/issues/999"
	decoded, err := decodeActionJournal(comment, "gh-999")
	if err != nil || !reflect.DeepEqual(a, decoded.ActionJournal) {
		t.Fatal("compressed import intent changed", err)
	}
}

// The journal's compact inventory must make the same decisions as a fresh
// remote plan, including receipts and interrupted writes, without another API
// request or fabricated observations for unrelated original tasks.
func TestImportCommandIntentReplansOriginalCheckpointDecisions(t *testing.T) {
	for _, stage := range []string{"allocation", "intent", "apply", "complete"} {
		t.Run(stage, func(t *testing.T) {
			c, native, _, comments := importBatchFixture(t)
			original := importTestItem("td-original")
			original.SourceNamespace = "checkpoint-project"
			base, stopped := c.run, false
			interrupted := errors.New("fixture lost response after remote write")
			c.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
				data, err := base(ctx, dir, payload, args...)
				match := stage == "allocation" && slices.Contains(args, "POST") && strings.HasSuffix(args[5], "/issues") ||
					stage == "intent" && strings.Contains(string(payload), "td:import-intent:") ||
					stage == "apply" && slices.Contains(args, "PATCH")
				if !stopped && match && err == nil {
					stopped = true
					return nil, interrupted
				}
				return data, err
			}
			_, err := c.executeImport(context.Background(), []ImportItem{original}, false)
			if stage == "complete" && err != nil || stage != "complete" && !errors.Is(err, interrupted) {
				t.Fatal("failed to arrange import checkpoint", err)
			}
			clock := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
			for _, issue := range native {
				issue["created_at"], issue["updated_at"] = clock, clock
			}
			for n := range *comments {
				(*comments)[n].CreatedAt, (*comments)[n].UpdatedAt = clock, clock
				(*comments)[n].User.Login = "original-author"
			}
			fresh := importTestItem("td-next")
			fresh.SourceNamespace = original.SourceNamespace
			items := []ImportItem{original, fresh}
			plan, err := c.PlanImport(context.Background(), items, false)
			if err != nil {
				t.Fatal(err)
			}
			wantAction, wantResume := "resume", ""
			switch stage {
			case "intent":
				wantResume = "before_apply"
			case "apply":
				wantResume = "after_apply"
			case "complete":
				wantAction = "skip"
			}
			if plan.Rows[0].Action != wantAction || plan.Rows[0].ResumeState != wantResume {
				t.Fatal("wrong arranged checkpoint", plan.Rows[0])
			}
			// Planning the frozen command must not fetch its evidence again.
			c.run = func(context.Context, string, []byte, ...string) ([]byte, error) {
				t.Fatal("original decision validation attempted remote access")
				return nil, nil
			}
			input, err := prepareImportCommand(items, false, plan, map[string][]nativeStateEvent{plan.Rows[0].TargetID: {}})
			if err != nil {
				t.Fatal(err)
			}
			a := ActionJournal{OperationID: "import-checkpoint", SessionID: "ses_import", CarrierID: "gh-999", Action: "import", Phase: "prepared", ImportCommand: input}
			if err := a.validate(); err != nil {
				t.Fatal(err)
			}
			if input.Members[0].Action == "skip" {
				input.Members[0].Action = "overwrite"
			} else {
				input.Members[0].Action = "skip"
			}
			if err := a.validate(); err == nil {
				t.Fatal("checkpoint decision was changed without changing original evidence")
			}
		})
	}
}

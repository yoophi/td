package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/reviewpolicy"
)

func workflowJournalCLI(t *testing.T) (func(...string) (string, string, error), func(...string) string) {
	t.Helper()
	_, _, run := journalCLIWithTimeout(t, 2*time.Minute)
	useNativeCommentOrderFixture(t)
	useHandoffReviewFixture(t)
	t.Setenv("TD_GH_FIXTURE_NATIVE_EVENTS", "1")
	return run, func(args ...string) string {
		t.Helper()
		out, stderr, err := run(append(args, "--json")...)
		if err != nil {
			t.Fatal(args, out, stderr, err)
		}
		return out
	}
}

func workflowLastGroup(t *testing.T, execute func(...string) string) ghstore.ActionJournalRecord {
	t.Helper()
	var rows []ghstore.ActionJournalRecord
	if out := execute("last", "--n", "1"); json.Unmarshal([]byte(out), &rows) != nil || len(rows) != 1 || rows[0].Group == nil || rows[0].Group.WorkflowUpdates == nil {
		t.Fatal("missing complete workflow command", out)
	}
	return rows[0]
}

func TestGitHubJournalCLIWorkflowFailureRecoveryDoesNotReplay(t *testing.T) {
	for _, mode := range []string{"target-lost", "receipt-denied", "comment-lost", "handoff-lost", "inverse-delete-lost", "inverse-receipt-denied"} {
		t.Run(mode, func(t *testing.T) {
			run, execute := workflowJournalCLI(t)
			execute("create", "Unrelated task", "--minor")
			args := []string{"create", "Workflow target"}
			if mode != "handoff-lost" {
				args = append(args, "--minor")
			}
			execute(args...)
			execute("log", "gh-6", "Unrelated activity survives")
			before := relationFixtureBytes(t, "TD_SCHEDULE_STATE")
			forwardFailure := !strings.HasPrefix(mode, "inverse-")
			status := "in_progress"
			if forwardFailure {
				failure := mode
				if mode == "comment-lost" || mode == "handoff-lost" {
					failure = "second-comment-lost"
				}
				if mode == "handoff-lost" {
					status = "in_review"
				}
				t.Setenv("TD_GH_JOURNAL_FAIL", failure)
			}
			out, stderr, err := run("update", "gh-7", "--title", "Saved command fields", "--status", status, "--comment", "Command activity", "--json")
			if (err != nil) != forwardFailure || forwardFailure && !strings.Contains(out+stderr, "lost") && !strings.Contains(out+stderr, "denied") {
				t.Fatal(out, stderr, err)
			}
			t.Setenv("TD_GH_JOURNAL_FAIL", "")
			group := workflowLastGroup(t, execute)
			if forwardFailure {
				actual := relationFixtureBytes(t, "TD_SCHEDULE_STATE")
				if _, _, err := run("undo", "--json"); err == nil {
					t.Fatal("uncertain forward command was replayed")
				}
				execute("undo", "--reconcile", group.ID, "--apply")
				if !bytes.Equal(actual, relationFixtureBytes(t, "TD_SCHEDULE_STATE")) {
					t.Fatal("forward recovery changed a target")
				}
			} else {
				failure := "delete-lost"
				if mode == "inverse-receipt-denied" {
					failure = "undo-completion-denied"
				}
				t.Setenv("TD_GH_JOURNAL_FAIL", failure)
				out, stderr, err := run("undo", "--json")
				if err == nil || !strings.Contains(out+stderr, "lost") && !strings.Contains(out+stderr, "denied") {
					t.Fatal(out, stderr, err)
				}
				t.Setenv("TD_GH_JOURNAL_FAIL", "")
				actual := relationFixtureBytes(t, "TD_SCHEDULE_STATE")
				if _, _, err := run("undo", "--json"); err == nil {
					t.Fatal("uncertain inverse continued without reconciliation")
				}
				execute("undo", "--reconcile", group.ID, "--apply")
				if !bytes.Equal(actual, relationFixtureBytes(t, "TD_SCHEDULE_STATE")) {
					t.Fatal("inverse recovery repeated target or comment write")
				}
			}
			execute("undo")
			var original, restored struct {
				Issues   map[string]map[string]any
				Comments []any
			}
			if json.Unmarshal(before, &original) != nil || json.Unmarshal(relationFixtureBytes(t, "TD_SCHEDULE_STATE"), &restored) != nil {
				t.Fatal("invalid fixture")
			}
			if !reflect.DeepEqual(original.Issues["6"], restored.Issues["6"]) || !reflect.DeepEqual(original.Comments, restored.Comments) {
				t.Fatal("unrelated task/activity changed or companion remained")
			}
			for _, field := range []string{"title", "labels", "state"} {
				if !reflect.DeepEqual(original.Issues["7"][field], restored.Issues["7"][field]) {
					t.Fatal("original field lost", field)
				}
			}
			left := fixtureWorkflowBodyWithoutReview(t, original.Issues["7"]["body"].(string), false)
			right := fixtureWorkflowBodyWithoutReview(t, restored.Issues["7"]["body"].(string), true)
			if !sameFixtureTaskContentsWithoutAudit(t, left, right) {
				t.Fatal("original task metadata lost")
			}
			actual := relationFixtureBytes(t, "TD_SCHEDULE_STATE")
			execute("undo", "--reconcile", group.ID, "--apply")
			if !bytes.Equal(actual, relationFixtureBytes(t, "TD_SCHEDULE_STATE")) {
				t.Fatal("completed confirmation wrote a target")
			}
			final := workflowLastGroup(t, execute)
			if final.Group.Inverse.Proof != "group-contents-observed" {
				t.Fatal("uncertainty promoted to verified inverse", final.Group.Inverse)
			}
		})
	}
}

func TestGitHubJournalCLIReviewAutomaticHandoffIsOneCommand(t *testing.T) {
	_, execute := workflowJournalCLI(t)
	execute("create", "Review target")
	execute("create", "Second review target")
	execute("review", "gh-6", "gh-7")
	group := workflowLastGroup(t, execute)
	if len(group.Group.Ordered.Forward) != 4 {
		t.Fatal("review omitted automatic handoff or root effect")
	}
	execute("undo")
	for _, id := range []string{"gh-6", "gh-7"} {
		var r struct{ Status string }
		if out := execute("show", id); json.Unmarshal([]byte(out), &r) != nil || r.Status != "open" {
			t.Fatal(out)
		}
	}
}

func TestGitHubJournalCLIWorkflowUnchangedBasicRequestDoesNotWrite(t *testing.T) {
	_, execute := workflowJournalCLI(t)
	execute("create", "Unchanged open task", "--minor")
	before, journals := relationFixtureBytes(t, "TD_SCHEDULE_STATE"), relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")
	execute("update", "gh-6", "--status", "open")
	if !bytes.Equal(before, relationFixtureBytes(t, "TD_SCHEDULE_STATE")) || !bytes.Equal(journals, relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")) {
		t.Fatal("unchanged status created a synthetic effect or journal")
	}
}

func TestGitHubJournalCLIWorkflowRejectsForeignChanges(t *testing.T) {
	for _, mode := range []string{"source", "watched-dependency", "handoff", "member-receipt", "native-events"} {
		t.Run(mode, func(t *testing.T) {
			run, execute := workflowJournalCLI(t)
			execute("create", "Watched dependency", "--minor")
			execute("create", "Review target")
			execute("update", "gh-7", "--title", "Saved fields", "--depends-on", "gh-6", "--status", "in_review", "--comment", "Companion comment")
			group := workflowLastGroup(t, execute)
			env := "TD_SCHEDULE_STATE"
			if mode == "member-receipt" || mode == "native-events" {
				env = "TD_GH_FIXTURE_JOURNAL"
			}
			var native map[string]any
			if err := json.Unmarshal(relationFixtureBytes(t, env), &native); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "source", "watched-dependency":
				id := "7"
				if mode == "watched-dependency" {
					id = "6"
				}
				native["issues"].(map[string]any)[id].(map[string]any)["title"] = "External edit at original clock"
			case "handoff":
				native["comments"].([]any)[0].(map[string]any)["body"] = "External handoff edit at original clock"
			case "member-receipt":
				member := group.Group.Ordered.Forward[0].Record
				number, err := ghstore.CommentNumber(member.ID)
				if err != nil {
					t.Fatal(err)
				}
				changed := false
				for _, raw := range native["comments"].([]any) {
					row := raw.(map[string]any)
					if row["id"] == float64(number) {
						body := row["body"].(string)
						if !strings.Contains(body, member.OperationID) {
							t.Fatal("member receipt does not contain its original operation")
						}
						row["body"] = strings.ReplaceAll(body, member.OperationID, "td-op-foreign-receipt")
						changed = true
						break
					}
				}
				if !changed {
					t.Fatal("original member receipt not found")
				}
			case "native-events":
				native["native_events"].(map[string]any)["7"] = []any{map[string]any{"id": 1, "event": "closed"}, map[string]any{"id": 2, "event": "reopened"}}
			}
			data, err := json.Marshal(native)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(os.Getenv(env), data, 0600); err != nil {
				t.Fatal(err)
			}
			state, journal := relationFixtureBytes(t, "TD_SCHEDULE_STATE"), relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")
			out, stderr, err := run("undo", "--json")
			if err == nil || !strings.Contains(out+stderr, "changed") && !strings.Contains(out+stderr, "unverified") && !strings.Contains(out+stderr, "conflict") {
				t.Fatal("foreign evidence was not explicitly rejected", out, stderr, err)
			}
			if !bytes.Equal(state, relationFixtureBytes(t, "TD_SCHEDULE_STATE")) || !bytes.Equal(journal, relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")) {
				t.Fatal("foreign evidence was rejected only after a target or journal write")
			}
		})
	}
}

func TestGitHubJournalCLIWorkflowFieldEditCannotUseEarlierApproval(t *testing.T) {
	run, execute := workflowJournalCLI(t)
	execute("create", "Reviewed task")
	execute("start", "gh-6")
	execute("review", "gh-6")
	execute("approve", "gh-6", "--record-only", "--self-review", "--reason", "Fixture review of original contents")
	before := relationFixtureBytes(t, "TD_SCHEDULE_STATE")
	out, stderr, err := run("update", "gh-6", "--priority", "P1", "--status", "closed", "--json")
	if err == nil || !strings.Contains(out+stderr, "review is stale") {
		t.Fatal("changed contents reused approval of the earlier contents", out, stderr, err)
	}
	group := workflowLastGroup(t, execute)
	if group.Phase != "prepared" || !strings.Contains(out+stderr, group.ID) || !strings.Contains(out+stderr, "field changes were saved") {
		t.Fatal("policy failure concealed its unfinished command or earlier saved fields", out, stderr)
	}
	var actual struct{ Status, Priority string }
	if out := execute("show", "gh-6"); json.Unmarshal([]byte(out), &actual) != nil || actual.Status != "in_review" || actual.Priority != "P1" {
		t.Fatal("policy failure lost its actual partial result", out)
	}
	saved := relationFixtureBytes(t, "TD_SCHEDULE_STATE")
	execute("undo", "--reconcile", group.ID, "--apply")
	if !bytes.Equal(saved, relationFixtureBytes(t, "TD_SCHEDULE_STATE")) {
		t.Fatal("recovering a partial result repeated a target write")
	}
	group = workflowLastGroup(t, execute)
	if group.Group.Forward == nil || !group.Group.Forward.Partial {
		t.Fatal("recovery concealed the incomplete command")
	}
	journals := relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")
	execute("undo", "--reconcile", group.ID, "--apply")
	if !bytes.Equal(saved, relationFixtureBytes(t, "TD_SCHEDULE_STATE")) || !bytes.Equal(journals, relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")) {
		t.Fatal("confirming a recovered partial result performed a write")
	}
	execute("undo")
	var original, restored struct {
		Issues   map[string]map[string]any
		Comments []any
	}
	if json.Unmarshal(before, &original) != nil || json.Unmarshal(relationFixtureBytes(t, "TD_SCHEDULE_STATE"), &restored) != nil {
		t.Fatal("invalid fixture")
	}
	if !reflect.DeepEqual(original.Comments, restored.Comments) {
		t.Fatal("partial command inverse removed an existing handoff")
	}
	for _, field := range []string{"title", "labels", "state"} {
		if !reflect.DeepEqual(original.Issues["6"][field], restored.Issues["6"][field]) {
			t.Fatal("partial command inverse lost an original field", field)
		}
	}
	left := fixtureWorkflowBodyWithoutReview(t, original.Issues["6"]["body"].(string), false)
	right := fixtureWorkflowBodyWithoutReview(t, restored.Issues["6"]["body"].(string), true)
	if !sameFixtureTaskContentsWithoutAudit(t, left, right) {
		t.Fatal("partial command inverse lost original task fields")
	}
}

func TestGitHubJournalCLIWorkflowApprovalIncludesParentAndDependent(t *testing.T) {
	_, execute := workflowJournalCLI(t)
	execute("create", "Parent epic", "--type", "epic")
	execute("create", "Reviewed child", "--parent", "gh-6")
	execute("create", "Blocked dependent", "--minor")
	execute("update", "gh-8", "--depends-on", "gh-7")
	execute("block", "gh-8")
	execute("review", "gh-7")
	execute("approve", "gh-7", "--record-only", "--self-review", "--reason", "Fixture review of child")
	before := relationFixtureBytes(t, "TD_SCHEDULE_STATE")
	// Points do not change the reviewed contents. This command may use the
	// recorded approval, and must include parent closure and dependent release.
	execute("update", "gh-7", "--points", "5", "--status", "closed", "--comment", "Close reviewed child")
	for id, status := range map[string]string{"gh-6": "closed", "gh-7": "closed", "gh-8": "open"} {
		var actual struct{ Status string }
		if out := execute("show", id); json.Unmarshal([]byte(out), &actual) != nil || actual.Status != status {
			t.Fatal("workflow cascade omitted a required target", id, out)
		}
	}
	group := workflowLastGroup(t, execute)
	if len(group.Group.Ordered.Forward) != 5 {
		t.Fatal("command did not include fields, approval, parent, dependent and comment")
	}
	execute("undo")
	var original, restored struct {
		Issues   map[string]map[string]any
		Comments []any
	}
	if json.Unmarshal(before, &original) != nil || json.Unmarshal(relationFixtureBytes(t, "TD_SCHEDULE_STATE"), &restored) != nil {
		t.Fatal("invalid fixture")
	}
	if !reflect.DeepEqual(original.Comments, restored.Comments) {
		t.Fatal("workflow undo did not preserve existing handoffs and remove its comment")
	}
	for _, id := range []string{"6", "7", "8"} {
		for _, field := range []string{"title", "labels", "state"} {
			if !reflect.DeepEqual(original.Issues[id][field], restored.Issues[id][field]) {
				t.Fatal("workflow cascade inverse lost original target", id, field)
			}
		}
		left := fixtureWorkflowBodyWithoutReview(t, original.Issues[id]["body"].(string), false)
		right := fixtureWorkflowBodyWithoutReview(t, restored.Issues[id]["body"].(string), true)
		if !sameFixtureTaskContentsWithoutAudit(t, left, right) {
			t.Fatal("workflow cascade inverse lost original task fields", id)
		}
	}
	saved, journals := relationFixtureBytes(t, "TD_SCHEDULE_STATE"), relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")
	execute("undo", "--reconcile", group.ID, "--apply")
	if !bytes.Equal(saved, relationFixtureBytes(t, "TD_SCHEDULE_STATE")) || !bytes.Equal(journals, relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")) {
		t.Fatal("completed native cascade confirmation wrote a target or receipt")
	}
}

// Exercise approval options through the public store boundary used by adapters.
// The CLI update command intentionally does not expose these approval flags.
func TestGitHubJournalWorkflowApprovalPreservesReviewOptions(t *testing.T) {
	for _, mode := range []string{"self-review-close", "attributed-record-only", "changes-requested"} {
		t.Run(mode, func(t *testing.T) {
			dir, actor, run := journalCLIWithTimeout(t, 2*time.Minute)
			useNativeCommentOrderFixture(t)
			useHandoffReviewFixture(t)
			t.Setenv("TD_GH_FIXTURE_NATIVE_EVENTS", "1")
			execute := func(args ...string) string {
				t.Helper()
				out, stderr, err := run(append(args, "--json")...)
				if err != nil {
					t.Fatal(args, out, stderr, err)
				}
				return out
			}
			execute("create", "Approval option fixture")
			execute("start", "gh-6")
			execute("review", "gh-6")
			before := relationFixtureBytes(t, "TD_SCHEDULE_STATE")
			ctx := context.Background()
			client, err := ghstore.Open(ctx, dir, &models.GitHubStoreConfig{Remote: "origin", Repo: "owner/repo"})
			if err != nil {
				t.Fatal(err)
			}
			if err := client.TrackActions(actor); err != nil {
				t.Fatal(err)
			}
			options := ghstore.TransitionOptions{SessionID: actor, Mode: reviewpolicy.ModeTrusted, SelfReview: true, Reason: "Synthetic review option verification"}
			wantStatus, wantDecision := models.StatusClosed, reviewpolicy.DecisionApproved
			if mode != "self-review-close" {
				options.RecordOnly, wantStatus = true, models.StatusInReview
			}
			if mode == "attributed-record-only" {
				options.SelfReview, options.ReviewedBy = false, "Synthetic fixture reviewer"
			}
			if mode == "changes-requested" {
				options.Decision, wantDecision = reviewpolicy.DecisionChangesRequested, reviewpolicy.DecisionChangesRequested
			}
			status, points := models.StatusClosed, 5
			result, err := client.UpdateWorkflowCommand(ctx, []string{"gh-6"}, ghstore.Changes{Status: &status, Points: &points}, ghstore.RelationChanges{}, options, nil)
			if err != nil || len(result) != 1 || result[0].Status != wantStatus || result[0].Points != points {
				t.Fatal("approval options did not produce their requested behavior", result, err)
			}
			details := result[0].Details
			if details == nil || len(details.Reviews) != 1 {
				t.Fatal("missing actual review")
			}
			review := details.Reviews[0]
			// SelfReview records the involved recording session even when the
			// review is attributed elsewhere; ReviewedBy preserves that credit.
			if review.Decision != wantDecision || !review.SelfReview || review.ReviewedBy != options.ReviewedBy || review.Summary != options.Reason {
				t.Fatal("saved review replaced requested attribution or decision", review)
			}
			group := workflowLastGroup(t, execute)
			if !reflect.DeepEqual(group.Group.WorkflowUpdates.Options, options) {
				t.Fatal("command lost its original options")
			}
			execute("undo")
			var original, restored struct {
				Issues map[string]map[string]any
			}
			if json.Unmarshal(before, &original) != nil || json.Unmarshal(relationFixtureBytes(t, "TD_SCHEDULE_STATE"), &restored) != nil {
				t.Fatal("invalid fixture")
			}
			left := fixtureWorkflowBodyWithoutReview(t, original.Issues["6"]["body"].(string), false)
			right := fixtureWorkflowBodyWithoutReview(t, restored.Issues["6"]["body"].(string), true)
			if !sameFixtureTaskContentsWithoutAudit(t, left, right) {
				t.Fatal("approval inverse lost original task fields")
			}
		})
	}
}

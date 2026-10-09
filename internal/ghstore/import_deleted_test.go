package ghstore

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/models"
)

func TestImportDeletedParentAndDependencyPreservesActiveReadBoundary(t *testing.T) {
	for _, lost := range []string{"none", "parent_patch", "child_patch", "completion"} {
		t.Run(lost, func(t *testing.T) {
			c, _, writes, comments := importBatchFixture(t)
			parent, child := importTestItem("td-parent"), importTestItem("td-child")
			parent.SourceNamespace, child.SourceNamespace = "project", "project"
			deleted := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
			parent.Issue.DeletedAt = &deleted
			parent.Issue.Status = models.StatusClosed
			child.Issue.ParentID = parent.Issue.ID
			child.Dependencies = []models.IssueDependency{{IssueID: child.Issue.ID, DependsOnID: parent.Issue.ID, RelationType: "depends_on"}}
			items := []ImportItem{parent, child}
			base := c.run
			failed := false
			failure := errors.New("deleted import response lost")
			c.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
				data, err := base(ctx, dir, payload, args...)
				match := lost == "parent_patch" && slices.Contains(args, "PATCH") && strings.HasSuffix(args[5], "/issues/4") || lost == "child_patch" && slices.Contains(args, "PATCH") && strings.HasSuffix(args[5], "/issues/5") || lost == "completion" && slices.Contains(args, "POST") && strings.Contains(string(payload), "td:import-receipt:")
				if !failed && err == nil && match {
					failed = true
					return nil, failure
				}
				return data, err
			}
			result, err := c.executeImport(context.Background(), items, false)
			if lost != "none" {
				if !failed || !errors.Is(err, failure) {
					t.Fatal("lost write was hidden/retried", result, err)
				}
				result, err = c.executeImport(context.Background(), items, false)
			}
			if err != nil {
				t.Fatal(result, err)
			}
			gotParent, err := c.GetIncludingDeleted(context.Background(), "gh-4")
			if err != nil || gotParent.DeletedAt == nil || !gotParent.DeletedAt.Equal(deleted) || gotParent.Status != models.StatusClosed {
				t.Fatal("deleted state lost", gotParent, err)
			}
			gotChild, err := c.Get(context.Background(), "gh-5")
			if err != nil || gotChild.ParentID != "gh-4" || !slices.Equal(gotChild.Details.Dependencies, []string{"gh-4"}) {
				t.Fatal("historical references lost", gotChild, err)
			}
			if _, err := c.Get(context.Background(), "gh-4"); err == nil || !strings.Contains(err.Error(), "deleted") {
				t.Fatal("ordinary Get admitted deleted task", err)
			}
			title := "Ordinary update"
			if _, err := c.Update(context.Background(), "gh-4", Changes{Title: &title}); err == nil {
				t.Fatal("ordinary Update admitted deleted task")
			}
			deletedParent := "gh-4"
			if _, err := c.Update(context.Background(), "gh-1", Changes{ParentID: &deletedParent}); err == nil {
				t.Fatal("ordinary parent mutation admitted deleted task")
			}
			if _, err := c.ChangeDependencyObserved(context.Background(), gotChild, "gh-4", true, "worker"); err == nil {
				// Existing edges are rejected before traversal too; use a new owner to
				// verify the target graph traversal still rejects deleted dependencies.
				t.Fatal("ordinary dependency accepted existing edge")
			}
			owner, err := c.Get(context.Background(), "gh-1")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.ChangeDependencyObserved(context.Background(), owner, "gh-4", true, "worker"); err == nil || !strings.Contains(err.Error(), "deleted") {
				t.Fatal("ordinary dependency traversal admitted deleted task", err)
			}
			saved := len(*comments)
			result, err = c.executeImport(context.Background(), items, false)
			if err != nil || result.Skipped != 2 || *writes != 4 || len(*comments) != saved {
				t.Fatal("deleted replay repeated writes", result, err, *writes)
			}
		})
	}
}

func TestImportOverwriteCanRestoreDeletedDestination(t *testing.T) {
	c, issues, _, _ := importBatchFixture(t)
	deleted := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	body, err := encodeBody("", metadata{Type: models.TypeTask, Priority: models.PriorityP2, Details: &IssueDetails{Status: models.StatusOpen, DeletedAt: &deleted}})
	if err != nil {
		t.Fatal(err)
	}
	issues[1]["body"] = body
	restoredBody, err := encodeBody("", metadata{Type: models.TypeTask, Priority: models.PriorityP2, Details: &IssueDetails{Status: models.StatusOpen}})
	if err != nil {
		t.Fatal(err)
	}
	native := apiIssue{Number: 1, State: "open", Title: "Restored", Body: restoredBody}
	record, err := native.record()
	if err != nil {
		t.Fatal(err)
	}
	source := ImportItem{Issue: record.Issue, GitHub: &ExportedIssue{Version: 1, Repository: c.repo, Record: *record, Body: restoredBody, NativeState: "open", Activity: []models.Activity{}}}
	result, err := c.executeImport(context.Background(), []ImportItem{source}, true)
	if err != nil || result.Completed != 1 {
		t.Fatal(result, err)
	}
	got, err := c.Get(context.Background(), "gh-1")
	if err != nil || got.DeletedAt != nil || got.Title != "Restored" {
		t.Fatal(got, err)
	}
}

func TestImportDetachedGraphCanRetainDeletedDependency(t *testing.T) {
	c, inputs, _, writes, _ := importDetachFixture(t)
	deleted := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	meta := inputs[0].GitHub.Record.meta
	details := *inputs[0].GitHub.Record.Details
	details.DeletedAt = &deleted
	meta.Details = &details
	body, err := encodeBody(inputs[0].Issue.Description, meta)
	if err != nil {
		t.Fatal(err)
	}
	native := apiIssue{Number: 1, State: "open", Title: inputs[0].Issue.Title, Body: body}
	record, err := native.record()
	if err != nil {
		t.Fatal(err)
	}
	inputs[0].Issue, inputs[0].GitHub.Record, inputs[0].GitHub.Body = record.Issue, *record, body
	base := c.run
	failed := false
	failure := errors.New("lost logically deleted intermediate response")
	c.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		data, err := base(ctx, dir, payload, args...)
		if !failed && err == nil && slices.Contains(args, "PATCH") {
			failed = true
			return nil, failure
		}
		return data, err
	}
	if _, err := c.executeImport(context.Background(), inputs, true); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	result, err := c.executeImport(context.Background(), inputs, true)
	if err != nil || *writes != 4 {
		t.Fatal(result, err, *writes)
	}
	a, err := c.GetIncludingDeleted(context.Background(), "gh-1")
	if err != nil || a.DeletedAt == nil || a.ParentID != "gh-2" {
		t.Fatal(a, err)
	}
	b, err := c.Get(context.Background(), "gh-2")
	if err != nil || !slices.Equal(b.Details.Dependencies, []string{"gh-1"}) {
		t.Fatal(b, err)
	}
	if _, err := c.Get(context.Background(), "gh-1"); err == nil {
		t.Fatal("intermediate context leaked into ordinary reads")
	}
	result, err = c.executeImport(context.Background(), inputs, true)
	if err != nil || result.Skipped != 2 || *writes != 4 {
		t.Fatal("deleted detached replay repeated writes", result, err)
	}
}

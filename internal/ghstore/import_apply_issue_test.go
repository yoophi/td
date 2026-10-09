package ghstore

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/models"
)

func TestImportApplyIssueFieldsAndLostPatchResume(t *testing.T) {
	for _, losePatchResponse := range []bool{false, true} {
		name := "success"
		if losePatchResponse {
			name = "lost_patch_response"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			source := importTestItem("td-source")
			source.SourceNamespace = "project"
			source.Issue.Status, source.Issue.Priority = models.StatusInProgress, models.PriorityP1
			source.Issue.Labels = []string{"bug"}
			source.Issue.ImplementerSession, source.Issue.CreatorSession = "original-worker", "original-creator"
			source.Issue.ReviewerSession, source.Issue.ClosedBySession = "original-reviewer", "original-closer"
			now := time.Now().UTC()
			source.Issue.ReviewedAt = &now
			due := "2026-12-01"
			source.Issue.DueDate = &due
			source.Files = []models.IssueFile{{ID: "file-old", IssueID: source.Issue.ID, FilePath: "main.go", Role: models.FileRoleImplementation}}
			c, issues, writes := hierarchyFixture(t)
			comments := []apiComment{}
			base := c.run
			failure := errors.New("fixture response lost after issue PATCH")
			c.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
				if strings.Contains(args[5], "/issues/comments?") {
					return json.Marshal([][]apiComment{comments})
				}
				if strings.HasSuffix(args[5], "/comments") && slices.Contains(args, "POST") {
					var fields map[string]string
					if err := json.Unmarshal(payload, &fields); err != nil {
						t.Fatal(err)
					}
					comment := apiComment{ID: int64(len(comments) + 1), Body: fields["body"], IssueURL: "https://api.github.com/repos/owner/repo/issues/4"}
					comments = append(comments, comment)
					return json.Marshal(comment)
				}
				data, err := base(ctx, dir, payload, args...)
				if losePatchResponse && slices.Contains(args, "PATCH") && err == nil {
					return nil, failure
				}
				return data, err
			}
			// Allocation uses the same minimal creation payload expected by the
			// import stage, with source identity embedded atomically.
			skeleton := models.Issue{Title: source.Issue.Title, Description: source.Issue.Description, Type: source.Issue.Type, Priority: source.Issue.Priority, Points: source.Issue.Points, Acceptance: source.Issue.Acceptance, Labels: source.Issue.Labels, Status: models.StatusOpen}
			allocated, err := c.createWithOperationID(ctx, &skeleton, importOperationID(source.SourceKey()))
			if err != nil || allocated.ID != "gh-4" {
				t.Fatal(allocated, err)
			}
			plan, err := c.PlanImport(ctx, []ImportItem{source}, false)
			if err != nil {
				t.Fatal(err)
			}
			updated, _, patched, err := c.applyImportIssue(ctx, source, plan.Rows[0], importRelations{})
			if losePatchResponse {
				if !errors.Is(err, failure) {
					t.Fatal("lost PATCH cause discarded", err)
				}
			} else if err != nil || !patched || updated.Status != models.StatusInProgress {
				t.Fatal("issue fields not applied", updated, patched, err)
			}
			if *writes != 2 || len(comments) != 1 {
				t.Fatal("unexpected allocation/PATCH/intent count", *writes, len(comments))
			}
			plan, err = c.PlanImport(ctx, []ImportItem{source}, false)
			if err != nil || plan.Rows[0].ResumeState != "after_apply" {
				t.Fatal("applied PATCH not recovered", plan, err)
			}
			updated, _, patched, err = c.applyImportIssue(ctx, source, plan.Rows[0], importRelations{})
			if err != nil || patched || *writes != 2 || len(comments) != 1 {
				t.Fatal("already applied PATCH or intent repeated", patched, err, *writes)
			}
			if updated.ImplementerSession != "" || updated.ReviewerSession != "" || updated.CreatorSession != "" || updated.ReviewedAt != nil || updated.ClosedBySession != "" {
				t.Fatal("historical actors became active workflow authority", updated)
			}
			if updated.PriorityLabelWarning() != "" || !slices.Contains(updated.Labels, "bug") || !slices.Contains(updated.Labels, "td:in_progress") || updated.DueDate == nil || *updated.DueDate != due || len(updated.Details.Files) != 1 || updated.Details.Files[0].IssueID != "gh-4" {
				t.Fatal("portable fields/mirrors lost", updated)
			}
			if len(updated.Details.Reviews) != 0 || len(updated.Details.Transitions) != 0 {
				t.Fatal("import granted active historical review records")
			}
			issues[4]["title"] = "Subsequent user edit"
			if _, err := c.PlanImport(ctx, []ImportItem{source}, true); err == nil {
				t.Fatal("force accepted an external edit after interrupted import")
			}
		})
	}
}

func TestImportRelationshipProjectionMapsParentsAndBothDependencyDirections(t *testing.T) {
	parent, child := importTestItem("td-parent"), importTestItem("td-child")
	parent.SourceNamespace, child.SourceNamespace = "project", "project"
	child.Issue.ParentID = parent.Issue.ID
	child.Dependencies = []models.IssueDependency{{DependsOnID: parent.Issue.ID, RelationType: "depends_on"}}
	parent.Dependencies = []models.IssueDependency{{DependsOnID: child.Issue.ID, RelationType: "blocks"}}
	items := []ImportItem{parent, child}
	mapping := map[string]string{parent.SourceKey(): "gh-9", child.SourceKey(): "gh-12"}
	relations, err := importRelationshipProjection(items, mapping)
	if err != nil || relations[child.SourceKey()].parent != "gh-9" || !slices.Equal(relations[child.SourceKey()].dependencies, []string{"gh-9"}) || len(relations[parent.SourceKey()].dependencies) != 0 {
		t.Fatal("source IDs or dependency direction lost", relations, err)
	}
	parent.Dependencies = append(parent.Dependencies, models.IssueDependency{DependsOnID: child.Issue.ID, RelationType: "depends_on"})
	if _, err := importRelationshipProjection([]ImportItem{parent, child}, mapping); err == nil {
		t.Fatal("dependency cycle accepted")
	}
	delete(mapping, child.SourceKey())
	if _, err := importRelationshipProjection(items, mapping); err == nil {
		t.Fatal("unallocated child accepted")
	}
	mapping[child.SourceKey()] = "gh-9"
	if _, err := importRelationshipProjection(items, mapping); err == nil {
		t.Fatal("multiple source IDs mapped to one target")
	}
}

func TestImportApplyRejectsEditedUnpreparedCreationBeforeIntentOrPatch(t *testing.T) {
	source := importTestItem("td-source")
	source.SourceNamespace = "project"
	c, issues, writes := importHierarchyFixture(t)
	created, err := c.createWithOperationID(context.Background(), &source.Issue, importOperationID(source.SourceKey()))
	if err != nil {
		t.Fatal(err)
	}
	issues[created.Number]["title"] = "Already edited before replanning"
	plan, err := c.PlanImport(context.Background(), []ImportItem{source}, false)
	if err != nil {
		t.Fatal(err)
	}
	before := *writes
	_, _, _, err = c.applyImportIssue(context.Background(), source, plan.Rows[0], importRelations{})
	var conflict *ConflictError
	if !errors.As(err, &conflict) || *writes != before {
		t.Fatal("unprepared edit overwritten or intent posted", err, *writes)
	}
}

func TestImportApplyIssueWritesMappedParentAndDependency(t *testing.T) {
	ctx := context.Background()
	parent, child := importTestItem("td-parent"), importTestItem("td-child")
	parent.SourceNamespace, child.SourceNamespace = "project", "project"
	child.Issue.ParentID = parent.Issue.ID
	child.Dependencies = []models.IssueDependency{{DependsOnID: parent.Issue.ID, RelationType: "depends_on"}}
	c, _, writes := hierarchyFixture(t)
	comments := []apiComment{}
	base := c.run
	c.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		if strings.Contains(args[5], "/issues/comments?") {
			return json.Marshal([][]apiComment{comments})
		}
		if strings.HasSuffix(args[5], "/comments") && slices.Contains(args, "POST") {
			var fields map[string]string
			if err := json.Unmarshal(payload, &fields); err != nil {
				t.Fatal(err)
			}
			comment := apiComment{ID: int64(len(comments) + 1), Body: fields["body"], IssueURL: "https://api.github.com/" + strings.TrimSuffix(args[5], "/comments")}
			comments = append(comments, comment)
			return json.Marshal(comment)
		}
		return base(ctx, dir, payload, args...)
	}
	mapping := map[string]string{}
	for _, source := range []ImportItem{parent, child} {
		skeleton := source.Issue
		skeleton.ParentID = ""
		allocated, err := c.createWithOperationID(ctx, &skeleton, importOperationID(source.SourceKey()))
		if err != nil {
			t.Fatal(err)
		}
		mapping[source.SourceKey()] = allocated.ID
	}
	relations, err := importRelationshipProjection([]ImportItem{parent, child}, mapping)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := c.PlanImport(ctx, []ImportItem{parent, child}, false)
	if err != nil {
		t.Fatal(err)
	}
	updated, _, patched, err := c.applyImportIssue(ctx, child, plan.Rows[1], relations[child.SourceKey()])
	if err != nil || !patched || *writes != 3 || updated.ParentID != mapping[parent.SourceKey()] || !slices.Equal(updated.Details.Dependencies, []string{mapping[parent.SourceKey()]}) {
		t.Fatal("parent/dependency IDs were not written to allocated targets", updated, patched, err, *writes)
	}
}

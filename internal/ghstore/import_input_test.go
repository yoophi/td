package ghstore

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/marcus/td/internal/models"
)

func TestImportCommandKeepsOriginalInputAcrossRemoteCalls(t *testing.T) {
	for _, boundary := range []string{"observation", "allocation"} {
		t.Run(boundary, func(t *testing.T) {
			c, _, writes, comments := importBatchFixture(t)
			parent, child := importTestItem("td-parent"), importTestItem("td-child")
			parent.SourceNamespace, child.SourceNamespace = "original", "original"
			child.Issue.ParentID = parent.Issue.ID
			child.Issue.DueDate = new("2026-10-20")
			child.Dependencies = []models.IssueDependency{{DependsOnID: parent.Issue.ID, RelationType: "depends_on"}}
			child.Handoffs = []models.Handoff{{ID: "original-handoff", Done: []string{"Original completed work"}}}
			child.Logs = []models.Log{{ID: "original-log", Message: "Original progress", Type: models.LogTypeProgress}}
			items := []ImportItem{parent, child}
			originalDigest, err := items[1].Digest()
			if err != nil {
				t.Fatal(err)
			}
			base, changed := c.run, false
			c.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
				data, err := base(ctx, dir, payload, args...)
				match := boundary == "observation" && slices.Contains(args, "GET") || boundary == "allocation" && slices.Contains(args, "POST") && strings.HasSuffix(args[5], "/issues")
				if !changed && match && err == nil {
					changed = true
					items[1].Issue.Title = "Changed after preparation"
					*items[1].Issue.DueDate = "2026-11-30"
					items[1].Dependencies[0].DependsOnID = child.Issue.ID
					items[1].Handoffs[0].Done[0] = "Changed historical work"
					items[1].Logs[0].Message = "Changed history"
				}
				return data, err
			}
			result, err := c.Import(context.Background(), items, false)
			if err != nil || !changed || result.Completed != 2 || *writes != 4 {
				t.Fatalf("command consumed caller changes after preparation: result=%+v writes=%d err=%v", result, *writes, err)
			}
			row := result.Plan.Rows[1]
			if row.Digest != originalDigest {
				t.Fatal("command replaced its original source digest", row.Digest, originalDigest)
			}
			r, err := c.Get(context.Background(), row.TargetID)
			if err != nil || r.Title != "Imported task" || r.DueDate == nil || *r.DueDate != "2026-10-20" || !slices.Equal(r.Details.Dependencies, []string{result.Plan.Rows[0].TargetID}) {
				t.Fatal("imported fields do not match the original input", r, err)
			}
			parts := map[int]importProvenancePart{}
			for _, comment := range *comments {
				part, err := decodeImportProvenance(comment.Body)
				if err != nil {
					t.Fatal(err)
				}
				if part != nil && part.Kind == "source" && part.SourceKey == row.SourceKey {
					parts[part.Index] = *part
				}
			}
			data, err := reassembleImportProvenance(parts)
			var preserved ImportItem
			if err != nil || json.Unmarshal(data, &preserved) != nil || preserved.Handoffs[0].Done[0] != "Original completed work" || preserved.Logs[0].Message != "Original progress" {
				t.Fatal("original source history was replaced after preparation", string(data), err)
			}
		})
	}
}

func TestImportPreviewKeepsOriginalSourceDigest(t *testing.T) {
	c, _, writes, _ := importBatchFixture(t)
	items := []ImportItem{importTestItem("td-one")}
	want, _ := items[0].Digest()
	base := c.run
	c.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		items[0].Issue.Title = "Changed while observing destinations"
		return base(ctx, dir, payload, args...)
	}
	plan, err := c.PlanImport(context.Background(), items, false)
	if err != nil || len(plan.Rows) != 1 || plan.Rows[0].Digest != want || *writes != 0 {
		t.Fatal("preview did not preserve the validated input", plan, err)
	}
}

func TestFrozenImportPreservesPortableSourceIdentity(t *testing.T) {
	c, _, _, _ := importBatchFixture(t)
	exported, err := c.ExportIssues(context.Background(), true, true)
	if err != nil || len(exported) == 0 {
		t.Fatal("could not prepare native export", err)
	}
	legacy := importTestItem("td-one")
	legacy.Handoffs = []models.Handoff{}
	legacy.Dependencies = []models.IssueDependency{}
	legacy.Logs = []models.Log{}
	legacy.Files = []models.IssueFile{}
	for _, item := range []ImportItem{legacy, {GitHub: &exported[0], Issue: exported[0].Record.Issue}} {
		want, err := item.Digest()
		if err != nil {
			t.Fatal(err)
		}
		frozen, err := freezeImportItems([]ImportItem{item})
		if err != nil {
			t.Fatal(err)
		}
		got, err := frozen[0].Digest()
		if err != nil || got != want {
			t.Fatal("freezing changed the portable source identity", got, want, err)
		}
	}
}

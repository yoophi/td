package ghstore

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/marcus/td/internal/models"
)

func creationGraphFixture() map[string]Record {
	rows := map[string]Record{}
	for _, id := range []string{"gh-1", "gh-2", "gh-3", "gh-4", "gh-5", "gh-6", "gh-7"} {
		rows[id] = Record{Issue: models.Issue{ID: id}, Details: &IssueDetails{Dependencies: []string{}}}
	}
	rows["gh-1"].Details.Dependencies = []string{"gh-2"}
	rows["gh-3"].Details.Dependencies = []string{"gh-4"}
	parent := rows["gh-5"]
	parent.ParentID = "gh-6"
	rows[parent.ID] = parent
	return rows
}

func TestCreationRelationsPlanFreezesBothDirectionsAndAncestors(t *testing.T) {
	rows := creationGraphFixture()
	before, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	depends, blocks := []string{"#1"}, []string{"3"}
	plan, err := planCreationRelations(rows, "#5", RelationChanges{DependsOn: &depends, Blocks: &blocks})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ParentID != "gh-5" || !slices.Equal(plan.Parents, []string{"gh-5", "gh-6"}) || !slices.Equal(plan.Watches, []string{"gh-1", "gh-2", "gh-3", "gh-4", "gh-5", "gh-6"}) {
		t.Fatal("incomplete original external scope", plan)
	}
	if !slices.Equal(plan.DependsOn, []string{"gh-1"}) || !slices.Equal(plan.Blocks, []string{"gh-3"}) || !slices.Equal(plan.Original["gh-3"], []string{"gh-4"}) {
		t.Fatal("complete creation inputs not preserved", plan)
	}
	after, err := json.Marshal(rows)
	if err != nil || string(before) != string(after) || depends[0] != "#1" || blocks[0] != "3" {
		t.Fatal("creation planning changed caller observations", err)
	}
	updates, err := plan.bindCreatedID("gh-8", rows)
	if err != nil || !reflect.DeepEqual(updates, map[string][]string{"gh-3": {"gh-4", "gh-8"}}) {
		t.Fatal("allocated identity did not bind complete reverse input", updates, err)
	}
	updates["gh-3"][0] = "gh-99"
	rows["gh-3"].Details.Dependencies[0] = "gh-99"
	depends[0], blocks[0] = "gh-99", "gh-99"
	if !slices.Equal(plan.Original["gh-3"], []string{"gh-4"}) || plan.DependsOn[0] != "gh-1" || plan.Blocks[0] != "gh-3" {
		t.Fatal("creation plan aliases mutable caller or bound output")
	}
	for _, invalid := range []string{"", creationGraphNode, "8", "gh-1", "gh-7"} {
		if _, err := plan.bindCreatedID(invalid, rows); err == nil {
			t.Fatal("accepted guessed, noncanonical or preexisting identity", invalid)
		}
	}
}

func TestCreationRelationsPlanRejectsInvalidGraphBeforeAllocation(t *testing.T) {
	for _, mode := range []string{"duplicate-dependency", "duplicate-reverse", "direct-cycle", "indirect-cycle", "missing", "deleted", "carrier", "dangling", "parent-cycle", "missing-ancestor", "identity"} {
		t.Run(mode, func(t *testing.T) {
			rows := creationGraphFixture()
			depends, blocks, parent := []string{"gh-1"}, []string{"gh-3"}, "gh-5"
			switch mode {
			case "duplicate-dependency":
				depends = []string{"1", "gh-1"}
			case "duplicate-reverse":
				blocks = []string{"#3", "gh-3"}
			case "direct-cycle":
				blocks = []string{"gh-1"}
			case "indirect-cycle":
				rows["gh-2"].Details.Dependencies = []string{"gh-3"}
			case "missing":
				depends = []string{"gh-99"}
			case "deleted":
				r := rows["gh-1"]
				now := time.Now().UTC()
				r.DeletedAt = &now
				rows[r.ID] = r
			case "carrier":
				r := rows["gh-3"]
				r.meta.EntityKind = "journal"
				rows[r.ID] = r
			case "dangling":
				rows["gh-2"].Details.Dependencies = []string{"gh-99"}
			case "parent-cycle", "missing-ancestor":
				r := rows["gh-6"]
				r.ParentID = "gh-5"
				if mode == "missing-ancestor" {
					r.ParentID = "gh-99"
				}
				rows[r.ID] = r
			case "identity":
				r := rows["gh-1"]
				r.ID = "gh-2"
				rows["gh-1"] = r
			}
			if _, err := planCreationRelations(rows, parent, RelationChanges{DependsOn: &depends, Blocks: &blocks}); err == nil {
				t.Fatal("invalid creation graph accepted", mode)
			}
		})
	}
}

func TestCreationRelationsPlanIgnoresUnrelatedBrokenGraph(t *testing.T) {
	rows := creationGraphFixture()
	rows["gh-7"].Details.Dependencies = []string{"gh-99"}
	depends := []string{"gh-1"}
	plan, err := planCreationRelations(rows, "", RelationChanges{DependsOn: &depends})
	if err != nil || !slices.Equal(plan.Watches, []string{"gh-1", "gh-2"}) || len(plan.Blocks) != 0 {
		t.Fatal("unrelated graph blocked creation", plan, err)
	}
	plan, err = planCreationRelations(rows, "", RelationChanges{})
	if err != nil || len(plan.Watches) != 0 || len(plan.DependsOn) != 0 {
		t.Fatal("empty request invented external membership", plan, err)
	}
}

func TestPrepareIssueCreationFreezesDependencyInputWithoutWrites(t *testing.T) {
	f := newJournalWriteFixture(t, "")
	calls := 0
	run := f.client.run
	f.client.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		calls++
		return run(ctx, dir, payload, args...)
	}
	issue := &models.Issue{Title: "Prepared creation", Description: "Original description", Status: models.StatusOpen, Type: models.TypeTask, Priority: models.PriorityP1, Labels: []string{"custom"}}
	depends := []string{"gh-1"}
	plan, err := f.client.prepareIssueCreation(context.Background(), issue, "td-op-create-fixture", depends)
	if err != nil || calls != 0 {
		t.Fatal("creation preparation performed an unexpected side effect", plan, calls, err)
	}
	issue.Title, issue.Description, issue.Labels[0], depends[0] = "Modified", "Modified", "foreign", "gh-99"
	description, meta, _, err := decodeBody(plan.input.Body)
	if err != nil || plan.input.Title != "Prepared creation" || description != "Original description" || meta.OperationID != "td-op-create-fixture" || meta.Details == nil || !slices.Equal(meta.Details.Dependencies, []string{"gh-1"}) || !slices.Contains(plan.input.Labels, "custom") || slices.Contains(plan.input.Labels, "foreign") {
		t.Fatal("creation input was not detached from caller state", plan, err)
	}
	for _, invalid := range [][]string{{"1"}, {"gh-1", "gh-1"}, {creationGraphNode}} {
		if _, err := f.client.prepareIssueCreation(context.Background(), issue, "td-op-create-invalid", invalid); err == nil || calls != 0 {
			t.Fatal("invalid creation input was accepted or caused side effects", invalid, err)
		}
	}
}

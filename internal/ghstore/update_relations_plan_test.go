package ghstore

import (
	"reflect"
	"slices"
	"testing"

	"github.com/marcus/td/internal/models"
)

func TestRelationUpdatePlanSharedTargets(t *testing.T) {
	records := map[string]Record{}
	for _, id := range []string{"gh-1", "gh-2", "gh-3", "gh-4"} {
		records[id] = Record{Issue: models.Issue{ID: id}, Details: &IssueDetails{Dependencies: []string{}}}
	}
	records["gh-4"].Details.Dependencies = []string{"gh-1", "gh-2", "gh-3"}
	blocks := []string{"gh-3"}
	plan, err := planRelationUpdates(records, []string{"1", "gh-2"}, RelationChanges{Blocks: &blocks})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(plan.Desired["gh-3"], []string{"gh-1", "gh-2"}) || !slices.Equal(plan.Desired["gh-4"], []string{"gh-3"}) {
		t.Fatal("shared reverse targets lost the complete requested delta", plan.Desired)
	}
	if !slices.Equal(plan.Blocked["gh-1"], []string{"gh-4"}) || !slices.Equal(plan.Blocked["gh-2"], []string{"gh-4"}) {
		t.Fatal("original reverse membership was not captured", plan.Blocked)
	}
	if !slices.Equal(plan.Related["gh-3"], []string{"gh-1", "gh-2"}) {
		t.Fatal("shared target observations missing", plan.Related)
	}
	if !slices.Equal(records["gh-4"].Details.Dependencies, []string{"gh-1", "gh-2", "gh-3"}) || len(records["gh-3"].Details.Dependencies) != 0 {
		t.Fatal("planning changed original observations")
	}
	// Replacing both directions follows the caller's selection order, including
	// a later source replacing an edge introduced by an earlier --blocks.
	depends := []string{"gh-3"}
	blocks = []string{}
	plan, err = planRelationUpdates(records, []string{"gh-1", "gh-2"}, RelationChanges{DependsOn: &depends, Blocks: &blocks})
	if err != nil || !reflect.DeepEqual(plan.Desired["gh-4"], []string{"gh-3"}) {
		t.Fatal("combined graph replacement", plan, err)
	}
}

func TestRelationUpdatePlanRejectsInvalidFinalSelection(t *testing.T) {
	records := map[string]Record{
		"gh-1": {Issue: models.Issue{ID: "gh-1"}, Details: &IssueDetails{}},
		"gh-2": {Issue: models.Issue{ID: "gh-2"}, Details: &IssueDetails{Dependencies: []string{"gh-1"}}},
	}
	depends, empty := []string{"gh-2"}, []string{}
	for name, tc := range map[string]struct {
		ids    []string
		change RelationChanges
	}{
		"missing later source": {[]string{"gh-1", "gh-3"}, RelationChanges{Blocks: &empty}},
		"canonical duplicate":  {[]string{"1", "gh-1"}, RelationChanges{Blocks: &empty}},
		"cycle":                {[]string{"gh-1"}, RelationChanges{DependsOn: &depends}},
		"self block":           {[]string{"gh-2"}, RelationChanges{Blocks: &depends}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := planRelationUpdates(records, tc.ids, tc.change); err == nil {
				t.Fatal("invalid selection or graph accepted")
			}
		})
	}
	records["gh-1"].Details.Dependencies = []string{"gh-999"}
	plan, err := planRelationUpdates(records, []string{"gh-1"}, RelationChanges{DependsOn: &empty})
	if err != nil || len(plan.Desired["gh-1"]) != 0 {
		t.Fatal("cannot clean an existing dangling edge", plan, err)
	}
}

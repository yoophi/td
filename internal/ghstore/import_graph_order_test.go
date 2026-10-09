package ghstore

import (
	"slices"
	"strings"
	"testing"

	"github.com/marcus/td/internal/models"
)

func TestImportGraphOrderRemovesOldEdgesBeforeReversing(t *testing.T) {
	// Original a -> b; final b -> a. Input b,a must write a first.
	plan := &ImportPlan{Rows: []ImportPlanRow{{SourceKey: "b", TargetID: "gh-2", Action: "overwrite"}, {SourceKey: "a", TargetID: "gh-1", Action: "overwrite"}}, snapshot: &Snapshot{records: []Record{
		{Issue: models.Issue{ID: "gh-1", ParentID: "gh-2"}, Details: &IssueDetails{Dependencies: []string{"gh-2"}}},
		{Issue: models.Issue{ID: "gh-2"}},
	}}}
	relations := map[string]importRelations{"a": {}, "b": {parent: "gh-1", dependencies: []string{"gh-1"}}}
	order, err := importGraphOrder(plan, relations)
	if err != nil || !slices.Equal(order, []int{1, 0}) {
		t.Fatal(order, err)
	}
	// Independent final DAGs can demand opposite write orders. This needs a
	// separate durable detach phase, not an unsafe cyclic intermediate write.
	plan.snapshot.records[0].ParentID = ""
	plan.snapshot.records[1].ParentID = "gh-1"
	relations = map[string]importRelations{"a": {parent: "gh-2"}, "b": {dependencies: []string{"gh-1"}}}
	if _, err := importGraphOrder(plan, relations); err == nil || !strings.Contains(err.Error(), "intermediate detachments") {
		t.Fatal("unsafe intermediate graph accepted", err)
	}
}

func TestImportGraphOrderRejectsUnappliedSkipRelationships(t *testing.T) {
	plan := &ImportPlan{Rows: []ImportPlanRow{{SourceKey: "a", TargetID: "gh-1", Action: "skip"}}, snapshot: &Snapshot{records: []Record{{Issue: models.Issue{ID: "gh-1"}}}}}
	if _, err := importGraphOrder(plan, map[string]importRelations{"a": {parent: "gh-2"}}); err == nil {
		t.Fatal("skip silently dropped mapped relationships")
	}
}

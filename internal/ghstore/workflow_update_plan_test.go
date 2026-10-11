package ghstore

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/reviewpolicy"
)

func workflowGraphFixture(count int) map[string]Record {
	records := map[string]Record{}
	for i := 1; i <= count; i++ {
		id := fmt.Sprintf("gh-%d", i)
		records[id] = Record{Issue: models.Issue{ID: id, Status: models.StatusOpen}, Details: &IssueDetails{}}
	}
	return records
}

func workflowGraphParent(records map[string]Record, id, parent string) {
	r := records[id]
	r.ParentID = parent
	records[id] = r
}

func assertWorkflowGraphMembers(t *testing.T, got, want []string) {
	t.Helper()
	a, b := slices.Clone(got), slices.Clone(want)
	slices.Sort(a)
	slices.Sort(b)
	if !slices.Equal(a, b) {
		t.Fatalf("membership = %v, want %v", a, b)
	}
}

func TestWorkflowUpdateGraphReviewPinsBothParentHierarchies(t *testing.T) {
	records := workflowGraphFixture(7)
	workflowGraphParent(records, "gh-1", "gh-3")
	workflowGraphParent(records, "gh-2", "gh-1")
	workflowGraphParent(records, "gh-4", "gh-3")
	workflowGraphParent(records, "gh-6", "gh-5")
	before, err := json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	status, parent := models.StatusInReview, "5"
	plan, err := planWorkflowUpdateGraph(records, []string{"1"}, Changes{Status: &status, ParentID: &parent}, RelationChanges{}, TransitionOptions{SessionID: "worker", Mode: reviewpolicy.ModeStrict})
	if err != nil {
		t.Fatal(err)
	}
	assertWorkflowGraphMembers(t, plan.Members, []string{"gh-1", "gh-2", "gh-3", "gh-4", "gh-5", "gh-6"})
	assertWorkflowGraphMembers(t, plan.BeforeScopes["gh-1"], []string{"gh-1", "gh-2", "gh-3", "gh-4"})
	assertWorkflowGraphMembers(t, plan.AfterScopes["gh-1"], []string{"gh-1", "gh-2", "gh-5", "gh-6"})
	after, err := json.Marshal(records)
	if err != nil || string(before) != string(after) {
		t.Fatal("planning mutated original observations", err)
	}
}

func TestWorkflowUpdateGraphNativeClosureSharedEdgesAndDependents(t *testing.T) {
	records := workflowGraphFixture(10)
	workflowGraphParent(records, "gh-1", "gh-3")
	records["gh-4"].Details.Dependencies = []string{"gh-3"}
	records["gh-6"].Details.Dependencies = []string{"gh-5", "gh-7"}
	records["gh-9"].Details.Dependencies = []string{"gh-1", "gh-2"}
	status, parent, blocks := models.StatusClosed, "gh-5", []string{"gh-8"}
	options := TransitionOptions{SessionID: "worker", Mode: reviewpolicy.ModeTrusted}
	plan, err := planWorkflowUpdateGraph(records, []string{"gh-1", "gh-2"}, Changes{Status: &status, ParentID: &parent}, RelationChanges{Blocks: &blocks}, options)
	if err != nil {
		t.Fatal(err)
	}
	assertWorkflowGraphMembers(t, plan.Members, []string{"gh-1", "gh-2", "gh-3", "gh-4", "gh-5", "gh-6", "gh-7", "gh-8", "gh-9"})
	if !slices.Equal(plan.Relations.Desired["gh-8"], []string{"gh-1", "gh-2"}) || len(plan.Relations.Desired["gh-9"]) != 0 {
		t.Fatal("shared relation replacements were not combined", plan.Relations.Desired)
	}
	if plan.Actions["gh-1"] != "close" || plan.Actions["gh-2"] != "close" {
		t.Fatal("native actions lost", plan.Actions)
	}
	// Map insertion order cannot change the original command membership.
	reversed := map[string]Record{}
	for i := 10; i > 0; i-- {
		id := fmt.Sprintf("gh-%d", i)
		reversed[id] = records[id]
	}
	again, err := planWorkflowUpdateGraph(reversed, []string{"gh-1", "gh-2"}, Changes{Status: &status, ParentID: &parent}, RelationChanges{Blocks: &blocks}, options)
	if err != nil || !reflect.DeepEqual(plan, again) {
		t.Fatal("membership or graph inputs depend on map order", err)
	}
}

func TestWorkflowUpdateGraphRetainsMixedActionsAndPolicyBoundary(t *testing.T) {
	records := workflowGraphFixture(4)
	for i, status := range []models.Status{models.StatusInProgress, models.StatusBlocked, models.StatusInReview, models.StatusClosed} {
		id := fmt.Sprintf("gh-%d", i+1)
		r := records[id]
		r.Status = status
		records[id] = r
	}
	status := models.StatusOpen
	options := TransitionOptions{SessionID: "worker", Mode: reviewpolicy.ModeStrict}
	plan, err := planWorkflowUpdateGraph(records, []string{"gh-1", "gh-2", "gh-3", "gh-4"}, Changes{Status: &status}, RelationChanges{}, options)
	want := map[string]string{"gh-1": "unstart", "gh-2": "unblock", "gh-3": "reject", "gh-4": "reopen"}
	if err != nil || !reflect.DeepEqual(plan.Actions, want) {
		t.Fatal("one requested status lost distinct original transitions", plan, err)
	}
	// Planning is not approval. A later content edit can revoke review, and
	// execution must report that policy failure after the fields were saved.
	status = models.StatusClosed
	title := "Changed after review"
	plan, err = planWorkflowUpdateGraph(records, []string{"gh-1", "gh-3"}, Changes{Status: &status, Title: &title}, RelationChanges{}, options)
	if err != nil || plan.Actions["gh-1"] != "close" || plan.Actions["gh-3"] != "approve" {
		t.Fatal("planning excluded native approval or changed its action", plan, err)
	}
}

func TestWorkflowUpdateGraphRejectsInvalidSelectedInputs(t *testing.T) {
	for _, scenario := range []string{"duplicate", "missing", "self-parent", "parent-cycle", "missing-parent", "foreign-claim", "blocked", "invalid-transition", "self-dependency", "carrier"} {
		t.Run(scenario, func(t *testing.T) {
			records := workflowGraphFixture(3)
			status := models.StatusInProgress
			change := Changes{Status: &status}
			ids := []string{"gh-1", "gh-2"}
			relations := RelationChanges{}
			switch scenario {
			case "duplicate":
				ids = []string{"gh-1", "1"}
			case "missing":
				ids[1] = "gh-99"
			case "self-parent":
				parent := "gh-2"
				change.ParentID = &parent
			case "parent-cycle":
				workflowGraphParent(records, "gh-3", "gh-1")
				parent := "gh-3"
				change.ParentID = &parent
			case "missing-parent":
				parent := "gh-99"
				change.ParentID = &parent
			case "foreign-claim", "blocked", "invalid-transition":
				r := records["gh-2"]
				r.Status = map[string]models.Status{"foreign-claim": models.StatusInProgress, "blocked": models.StatusBlocked, "invalid-transition": models.StatusClosed}[scenario]
				r.ImplementerSession = "peer"
				records["gh-2"] = r
			case "self-dependency":
				depends := []string{"gh-2"}
				relations.DependsOn = &depends
			case "carrier":
				r := records["gh-2"]
				r.meta.EntityKind = "journal"
				records["gh-2"] = r
			}
			if _, err := planWorkflowUpdateGraph(records, ids, change, relations, TransitionOptions{SessionID: "worker"}); err == nil {
				t.Fatal("invalid complete command was accepted")
			}
		})
	}
}

func TestWorkflowUpdateGraphAllowsRepairAndWatchesRemovedLiveEdges(t *testing.T) {
	for _, broken := range []string{"parent-cycle", "missing-parent", "missing-dependency"} {
		t.Run(broken, func(t *testing.T) {
			records := workflowGraphFixture(4)
			workflowGraphParent(records, "gh-1", "gh-2")
			records["gh-1"].Details.Dependencies = []string{"gh-3"}
			records["gh-3"].Details.Dependencies = []string{"gh-4"}
			switch broken {
			case "parent-cycle":
				workflowGraphParent(records, "gh-2", "gh-1")
			case "missing-parent":
				workflowGraphParent(records, "gh-2", "gh-99")
			case "missing-dependency":
				records["gh-3"].Details.Dependencies = []string{"gh-4", "gh-99"}
			}
			status, parent, dependencies := models.StatusInReview, "", []string{}
			plan, err := planWorkflowUpdateGraph(records, []string{"gh-1"}, Changes{Status: &status, ParentID: &parent}, RelationChanges{DependsOn: &dependencies}, TransitionOptions{SessionID: "worker", Mode: reviewpolicy.ModeTrusted})
			if err != nil {
				t.Fatal("valid repair rejected because of original graph corruption", err)
			}
			assertWorkflowGraphMembers(t, plan.Members, []string{"gh-1", "gh-2", "gh-3", "gh-4"})
			if len(plan.Relations.Desired["gh-1"]) != 0 {
				t.Fatal("old edges leaked into requested repair")
			}
		})
	}
}

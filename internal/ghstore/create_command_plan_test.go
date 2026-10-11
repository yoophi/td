package ghstore

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/marcus/td/internal/models"
)

func TestRelatedCreationPreparationUsesOneBulkReadAndOwnsInputs(t *testing.T) {
	f := newJournalWriteFixture(t, "")
	depends := []string{}
	for n := 20; n < 80; n++ {
		r := f.issues[1]
		r.Number, r.Title = n, fmt.Sprintf("Existing %d", n)
		details := IssueDetails{Status: models.StatusOpen}
		if n == 20 || n == 21 {
			details.ParentID = fmt.Sprintf("gh-%d", n+1)
		}
		body, err := encodeBody("Existing task", metadata{Type: models.TypeTask, Priority: models.PriorityP2, Details: &details})
		if err != nil {
			t.Fatal(err)
		}
		r.Body = body
		f.issues[n] = r
		if n >= 23 {
			depends = append(depends, fmt.Sprintf("gh-%d", n))
		}
	}
	blocks := []string{"gh-1"}
	issue := &models.Issue{Title: "Frozen title", Type: models.TypeTask, Priority: models.PriorityP2, ParentID: "gh-20", Labels: []string{"original"}}
	requests := []string{}
	run := f.client.run
	f.client.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		requests = append(requests, args[4]+" "+args[5])
		// Simulate caller reuse while the network operation is pending. No
		// requested field may be read from these caller-owned values afterward.
		issue.Title, issue.Labels[0], depends[0], blocks[0] = "Peer reuse", "foreign", "gh-999", "gh-999"
		return run(ctx, dir, payload, args...)
	}
	plan, err := f.client.prepareRelatedCreation(context.Background(), issue, RelationChanges{DependsOn: &depends, Blocks: &blocks}, "td-op-creation-fixture")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(requests, []string{"GET repos/owner/repo/issues?state=all&per_page=100"}) {
		t.Fatal("creation preparation enumerated targets or performed a write", requests)
	}
	if len(plan.watches) != 61 || len(plan.create.parents) != 3 || len(plan.graph.DependsOn) != 57 || len(plan.records) != 61 {
		t.Fatal("preparation omitted actual external observations", len(plan.watches), len(plan.records), plan.graph)
	}
	if plan.create.input.Title != "Frozen title" || !slices.Contains(plan.create.input.Labels, "original") || plan.graph.DependsOn[0] != "gh-23" || !slices.Equal(plan.graph.Blocks, []string{"gh-1"}) {
		t.Fatal("caller reuse changed the frozen command input", plan)
	}
	for _, watch := range plan.watches {
		if err := watch.State.validate(); err != nil || !canonicalGroupID(watch.ID) || watch.State.UpdatedAt.IsZero() {
			t.Fatal("watched state was projected instead of observed", watch.ID, err)
		}
	}
	if strings.Contains(plan.create.input.Body, creationGraphNode) {
		t.Fatal("graph-only new node escaped into native creation input")
	}
}

func TestRelatedCreationPreparationRejectsInvalidInputWithoutWrites(t *testing.T) {
	for _, mode := range []string{"identified", "non-open", "invalid-fields", "missing-dependency", "carrier-parent", "creation-cycle"} {
		t.Run(mode, func(t *testing.T) {
			f := newJournalWriteFixture(t, "")
			issue := &models.Issue{Title: "New task", Type: models.TypeTask, Priority: models.PriorityP2}
			depends, blocks := []string{"gh-1"}, []string{}
			switch mode {
			case "identified":
				issue.ID = "gh-10"
			case "non-open":
				issue.Status = models.StatusInProgress
			case "invalid-fields":
				issue.Title = ""
			case "missing-dependency":
				depends = []string{"gh-99"}
			case "carrier-parent":
				issue.ParentID = "gh-9"
			case "creation-cycle":
				blocks = []string{"gh-1"}
			}
			writes := 0
			run := f.client.run
			f.client.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
				if args[4] != "GET" {
					writes++
				}
				return run(ctx, dir, payload, args...)
			}
			if _, err := f.client.prepareRelatedCreation(context.Background(), issue, RelationChanges{DependsOn: &depends, Blocks: &blocks}, "td-op-invalid-creation"); err == nil || writes != 0 {
				t.Fatal("invalid creation request accepted or wrote", mode, err, writes)
			}
		})
	}
}

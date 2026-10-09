package ghstore

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/marcus/td/internal/reviewpolicy"
)

func TestReviewCascadeSharesInitialHierarchyObservation(t *testing.T) {
	c, _, _ := parentFixture(t)
	base := c.run
	initialListings, listings := 0, 0
	wrote := false
	c.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		if strings.Contains(args[5], "?state=") {
			listings++
			if !wrote {
				initialListings++
			}
		}
		if slices.Contains(args, "PATCH") || slices.Contains(args, "POST") {
			wrote = true
		}
		return base(ctx, dir, payload, args...)
	}
	result, _, err := c.TransitionWithCascades(context.Background(), "3", "review", TransitionOptions{SessionID: "fixture-requester", Mode: reviewpolicy.ModeTrusted})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ParentStatusUpdates) != 2 {
		t.Fatalf("parent cascade missing: %+v", result)
	}
	if initialListings != 1 {
		t.Fatalf("initial hierarchy listings = %d; want one shared fresh observation", initialListings)
	}
	// Both parent writes still require fresh before/after graph observations.
	if listings < 6 {
		t.Fatalf("post-write conflict checks disappeared: %d listings", listings)
	}
}

package ghstore

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestDependencyBatchPreflightNoopsAndMissingTargetRemoval(t *testing.T) {
	ctx := context.Background()
	c, issues, writes := hierarchyFixture(t)
	source, err := c.Get(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	for _, targets := range [][]string{{"2", "999"}, {"2", "owner/other#3"}, {"2", "1"}} {
		if _, _, err := c.ChangeDependenciesObserved(ctx, source, targets, true, "ses_actor"); err == nil || *writes != 0 {
			t.Fatal("invalid later target changed source", targets, err)
		}
	}
	result, changes, err := c.ChangeDependenciesObserved(ctx, source, []string{"2", "gh-2", "3"}, true, "ses_actor")
	if err != nil || *writes != 1 || len(changes) != 3 || changes[0].AlreadyExists || !changes[1].AlreadyExists || changes[2].AlreadyExists || !slices.Equal(result.Details.Dependencies, []string{"gh-2", "gh-3"}) {
		t.Fatal("complete delta or duplicate-add contract lost", changes, err)
	}
	if source.Details != nil && len(source.Details.Dependencies) != 0 {
		t.Fatal("original observation was mutated")
	}
	for _, targets := range [][]string{{"2", "999"}, {"2", "gh-2"}} {
		if _, _, err := c.ChangeDependenciesObserved(ctx, result, targets, false, "ses_actor"); err == nil || *writes != 1 {
			t.Fatal("invalid later removal changed source", targets, err)
		}
	}
	if _, changes, err = c.ChangeDependenciesObserved(ctx, result, []string{"2", "3"}, true, "ses_actor"); err != nil || *writes != 1 || !changes[0].AlreadyExists || !changes[1].AlreadyExists {
		t.Fatal("already-present edges were written", changes, err)
	}
	issues[1]["title"] = "Foreign same-clock edit"
	if _, _, err := c.ChangeDependenciesObserved(ctx, result, []string{"2", "3"}, true, "ses_actor"); err == nil || *writes != 1 {
		t.Fatal("no-op accepted stale source", err)
	}
	result, err = c.Get(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	delete(issues, 2)
	target, err := c.Get(ctx, "3")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.SetDeletedObserved(ctx, target, true, "ses_actor", "fixture"); err != nil {
		t.Fatal(err)
	}
	before := *writes
	result, _, err = c.ChangeDependenciesObserved(ctx, result, []string{"2", "3"}, false, "ses_actor")
	if err != nil || *writes != before+1 || len(result.Details.Dependencies) != 0 {
		t.Fatal("missing/deleted targets prevented edge cleanup", err)
	}
}

func TestDependencyBatchSharesGraphAndKeepsRevisionFences(t *testing.T) {
	for _, mode := range []string{"success", "before", "after"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			c, issues, writes := hierarchyFixture(t)
			issues[4] = map[string]any{"number": 4, "state": "open", "title": "Shared reachable task"}
			setFixtureDependencies(t, c, "2", "gh-4")
			setFixtureDependencies(t, c, "3", "gh-4")
			source, err := c.Get(ctx, "1")
			if err != nil {
				t.Fatal(err)
			}
			before, sharedReads := *writes, 0
			base := c.run
			c.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
				if slices.Contains(args, "GET") && strings.HasSuffix(args[5], "/issues/4") {
					sharedReads++
					if mode == "before" && sharedReads == 2 || mode == "after" && *writes > before {
						issues[4]["title"] = "Concurrent graph edit"
					}
				}
				return base(ctx, dir, payload, args...)
			}
			_, _, err = c.ChangeDependenciesObserved(ctx, source, []string{"2", "3"}, true, "ses_actor")
			if mode == "success" {
				if err != nil || *writes != before+1 || sharedReads != 3 {
					t.Fatal("shared graph was repeatedly traversed or source written more than once", sharedReads, *writes-before, err)
				}
				return
			}
			var conflict *ConflictError
			want := before
			if mode == "after" {
				want++
			}
			if !errors.As(err, &conflict) || conflict.AfterWrite != (mode == "after") || *writes != want {
				t.Fatal("batch lost a graph revision fence", *writes-before, err)
			}
		})
	}
}

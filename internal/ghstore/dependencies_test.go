package ghstore

import (
	"context"
	"errors"
	"github.com/marcus/td/internal/models"
	"slices"
	"strings"
	"testing"
	"time"
)

func setFixtureDependencies(t *testing.T, c *Client, id string, deps ...string) {
	t.Helper()
	r, err := c.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	d, err := r.CopyDetails()
	if err != nil {
		t.Fatal(err)
	}
	d.Dependencies = deps
	if _, err := c.UpdateObserved(context.Background(), r, Changes{Details: &d}); err != nil {
		t.Fatal(err)
	}
}
func TestDependencyGraphMutationCycleDuplicateAndMissingBoundaries(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []string{"success", "diamond", "self", "cycle", "existing-cycle", "duplicate", "missing", "broken-graph", "deleted-target"} {
		t.Run(mode, func(t *testing.T) {
			c, issues, writes := hierarchyFixture(t)
			target := "2"
			switch mode {
			case "success":
				setFixtureDependencies(t, c, "2", "gh-3")
			case "diamond":
				issues[4] = map[string]any{"number": 4, "state": "open", "title": "Shared target fixture"}
				setFixtureDependencies(t, c, "2", "gh-3", "gh-4")
				setFixtureDependencies(t, c, "3", "gh-4")
			case "self":
				target = "1"
			case "cycle":
				setFixtureDependencies(t, c, "2", "gh-3")
				setFixtureDependencies(t, c, "3", "gh-1")
			case "existing-cycle":
				setFixtureDependencies(t, c, "2", "gh-3")
				setFixtureDependencies(t, c, "3", "gh-2")
			case "duplicate":
				setFixtureDependencies(t, c, "1", "gh-2")
			case "missing":
				target = "999"
			case "broken-graph":
				setFixtureDependencies(t, c, "2", "gh-999")
			case "deleted-target":
				r, err := c.Get(ctx, "2")
				if err != nil {
					t.Fatal(err)
				}
				if _, _, err := c.SetDeletedObserved(ctx, r, true, "fixture-deleter", ""); err != nil {
					t.Fatal(err)
				}
			}
			source, err := c.Get(ctx, "1")
			if err != nil {
				t.Fatal(err)
			}
			before := *writes
			result, err := c.ChangeDependencyObserved(ctx, source, target, true, "fixture-web")
			if mode != "success" && mode != "diamond" {
				if err == nil || *writes != before {
					t.Fatalf("invalid graph wrote: %s %+v %v", mode, result, err)
				}
				return
			}
			if err != nil || result.Status != source.Status || !slices.Equal(result.Details.Dependencies, []string{"gh-2"}) || *writes != before+1 {
				t.Fatalf("add: %+v %v", result, err)
			}
			if source.Details != nil && len(source.Details.Dependencies) != 0 {
				t.Fatal("observation mutated")
			}
			event := result.Details.Transitions[len(result.Details.Transitions)-1]
			if event.Action != "add_dep" || event.RelatedIssueID != "gh-2" || event.SessionID != "fixture-web" {
				t.Fatalf("history: %+v", event)
			}
		})
	}
}
func TestDependencyRemoveMissingTargetAndApprovalInvalidation(t *testing.T) {
	ctx := context.Background()
	c, issues, _ := hierarchyFixture(t)
	source, err := c.Get(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	d, err := source.CopyDetails()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	d.Status = models.StatusInReview
	d.Sprint = "fixture-sprint"
	d.ImplementerSession = "fixture-implementer"
	d.ReviewerSession = "fixture-reviewer"
	d.ReviewedAt = &now
	d.ReviewBasis = "fixture-basis"
	d.Reviews = []models.IssueReview{{ID: "fixture-review", ReviewerSession: "fixture-reviewer", Decision: "approved", CreatedAt: now}}
	source, err = c.UpdateObserved(ctx, source, Changes{Details: &d})
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.ChangeDependencyObserved(ctx, source, "2", true, "fixture-web")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != models.StatusInReview || result.ImplementerSession != d.ImplementerSession || result.Details.Sprint != d.Sprint || result.ReviewerSession != "" || result.ReviewedAt != nil || result.Details.ReviewBasis != "" || result.Details.Reviews[0].SupersededAt == nil {
		t.Fatalf("approval invalidation/preservation: %+v", result)
	}
	delete(issues, 2)
	restored, err := c.ChangeDependencyObserved(ctx, result, "2", false, "fixture-web")
	if err != nil || len(restored.Details.Dependencies) != 0 || restored.Details.Transitions[len(restored.Details.Transitions)-1].Action != "remove_dep" {
		t.Fatalf("missing target cleanup: %+v %v", restored, err)
	}
	if _, err := c.ChangeDependencyObserved(ctx, restored, "2", false, "fixture-web"); err == nil {
		t.Fatal("missing edge accepted")
	}
}
func TestDependencyObservedGraphConflictsBeforeAndAfterWrite(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(map[bool]string{false: "before", true: "after"}[after], func(t *testing.T) {
			ctx := context.Background()
			c, issues, writes := hierarchyFixture(t)
			source, err := c.Get(ctx, "1")
			if err != nil {
				t.Fatal(err)
			}
			base := c.run
			reads := 0
			c.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
				if slices.Contains(args, "GET") && slices.Contains(args, "repos/owner/repo/issues/2") {
					reads++
					if (!after && reads == 2) || (after && *writes > 0) {
						issues[2]["title"] = "External target graph edit"
					}
				}
				return base(ctx, dir, payload, args...)
			}
			_, err = c.ChangeDependencyObserved(ctx, source, "2", true, "fixture-web")
			var conflict *ConflictError
			want := 0
			if after {
				want = 1
			}
			if !errors.As(err, &conflict) || conflict.AfterWrite != after || *writes != want {
				t.Fatalf("conflict: writes %d err %v", *writes, err)
			}
			if after && !strings.Contains(err.Error(), "was saved") {
				t.Fatal("saved edge not reported")
			}
		})
	}
}
func TestDependencySourceStaleDeletedAndPermissionFailures(t *testing.T) {
	ctx := context.Background()
	c, issues, writes := hierarchyFixture(t)
	r, err := c.Get(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ChangeDependencyObserved(ctx, r, "2", true, ""); err == nil || *writes != 0 {
		t.Fatal("empty actor wrote")
	}
	issues[1]["title"] = "External source edit"
	if _, err := c.ChangeDependencyObserved(ctx, r, "2", true, "fixture-web"); err == nil || *writes != 0 {
		t.Fatal("stale source wrote")
	}
	r, err = c.Get(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	deleted, _, err := c.SetDeletedObserved(ctx, r, true, "fixture-web", "")
	if err != nil {
		t.Fatal(err)
	}
	before := *writes
	if _, err := c.ChangeDependencyObserved(ctx, deleted, "2", true, "fixture-web"); err == nil || *writes != before {
		t.Fatal("deleted source wrote")
	}
	c, _, writes = hierarchyFixture(t)
	r, err = c.Get(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	base := c.run
	attempts := 0
	c.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		if slices.Contains(args, "PATCH") {
			attempts++
			return nil, errors.New("fixture denied")
		}
		return base(ctx, dir, payload, args...)
	}
	if _, err := c.ChangeDependencyObserved(ctx, r, "2", true, "fixture-web"); err == nil || !strings.Contains(err.Error(), "fixture denied") || attempts != 1 || *writes != 0 {
		t.Fatalf("permission or retry: %v", err)
	}
}
func TestDependencyHistoryValidation(t *testing.T) {
	now := time.Now().UTC()
	for _, related := range []string{"", "2", "gh-0", "gh-2"} {
		d := IssueDetails{Transitions: []TransitionRecord{{Action: "add_dep", RelatedIssueID: related, SessionID: "fixture-web", OperationID: "fixture-op", From: models.StatusOpen, To: models.StatusOpen, At: now}}}
		if err := d.validate(); (err == nil) != (related == "gh-2") {
			t.Fatalf("related %q: %v", related, err)
		}
	}
}

func TestReplaceDependenciesDoesNotDiscardOldEdgesOnInvalidInput(t *testing.T) {
	for _, mode := range []string{"replace", "clear", "missing", "cycle", "duplicate", "stale"} {
		t.Run(mode, func(t *testing.T) {
			c, issues, writes := hierarchyFixture(t)
			ctx := context.Background()
			setFixtureDependencies(t, c, "1", "gh-2")
			if mode == "cycle" {
				setFixtureDependencies(t, c, "3", "gh-1")
			}
			observed, err := c.Get(ctx, "1")
			if err != nil {
				t.Fatal(err)
			}
			before := *writes
			targets := []string{"gh-3"}
			switch mode {
			case "clear":
				targets = nil
			case "missing":
				targets = []string{"gh-99"}
			case "duplicate":
				targets = []string{"3", "gh-3"}
			case "stale":
				issues[1]["title"] = "Concurrent source edit"
			}
			result, noop, err := c.ReplaceDependenciesObserved(ctx, observed, targets, "synthetic-actor")
			if mode != "replace" && mode != "clear" {
				if err == nil || *writes != before {
					t.Fatal("invalid replacement wrote", mode, result, err)
				}
				current, _ := c.Get(ctx, "1")
				if !slices.Equal(current.Details.Dependencies, []string{"gh-2"}) {
					t.Fatal("lost old dependency")
				}
				return
			}
			if err != nil || noop || *writes != before+1 || !slices.Equal(result.Details.Dependencies, targets) {
				t.Fatal(result, noop, err)
			}
			if !slices.Equal(observed.Details.Dependencies, []string{"gh-2"}) {
				t.Fatal("mutated observation")
			}
			if result.Details.Transitions[len(result.Details.Transitions)-1].SessionID != "synthetic-actor" {
				t.Fatal("actor lost")
			}
		})
	}
}

func TestReplaceDependencyGraphConflictIsExplicitBeforeAndAfterWrite(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(map[bool]string{false: "before", true: "after"}[after], func(t *testing.T) {
			c, issues, writes := hierarchyFixture(t)
			ctx := context.Background()
			r, _ := c.Get(ctx, "1")
			base := c.run
			reads := 0
			c.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
				if slices.Contains(args, "GET") && slices.Contains(args, "repos/owner/repo/issues/2") {
					reads++
					if (!after && reads == 2) || (after && *writes > 0) {
						issues[2]["title"] = "Concurrent target edit"
					}
				}
				return base(ctx, dir, payload, args...)
			}
			_, _, err := c.ReplaceDependenciesObserved(ctx, r, []string{"2"}, "synthetic-actor")
			var conflict *ConflictError
			if !errors.As(err, &conflict) || conflict.AfterWrite != after {
				t.Fatal(err)
			}
			if (!after && *writes != 0) || (after && *writes != 1) {
				t.Fatal("writes", *writes)
			}
			if after && !strings.Contains(err.Error(), "were saved") {
				t.Fatal("missing applied-state warning", err)
			}
		})
	}
}

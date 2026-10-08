package ghstore

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/reviewpolicy"
)

func parentFixture(t *testing.T) (*Client, map[int]map[string]any, *int) {
	t.Helper()
	c, issues, writes := hierarchyFixture(t)
	base := c.run
	c.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		if strings.HasSuffix(args[5], "/events?per_page=100") {
			return []byte(`[[]]`), nil
		}
		return base(ctx, dir, payload, args...)
	}
	epic, minor := models.TypeEpic, true
	for _, id := range []string{"1", "2"} {
		if _, err := c.Update(context.Background(), id, Changes{Type: &epic}); err != nil {
			t.Fatal(err)
		}
	}
	for id, parent := range map[string]string{"2": "gh-1", "3": "gh-2"} {
		p := parent
		if _, err := c.Update(context.Background(), id, Changes{ParentID: &p}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.Update(context.Background(), "3", Changes{Minor: &minor}); err != nil {
		t.Fatal(err)
	}
	return c, issues, writes
}

func TestParentCascadeReviewThenCloseWithoutFabricatingApproval(t *testing.T) {
	c, issues, _ := parentFixture(t)
	ctx := context.Background()
	parent, err := c.Get(ctx, "2")
	if err != nil {
		t.Fatal(err)
	}
	d, err := parent.CopyDetails()
	if err != nil {
		t.Fatal(err)
	}
	d.Status, d.ImplementerSession = models.StatusBlocked, "fixture-original-worker"
	if _, err := c.UpdateObserved(ctx, parent, Changes{Details: &d}); err != nil {
		t.Fatal(err)
	}
	body, err := encodeBody("Dependent fixture", metadata{Type: models.TypeTask, Priority: models.PriorityP2, Details: &IssueDetails{Status: models.StatusBlocked, ImplementerSession: "fixture-dependent-worker", Dependencies: []string{"gh-1"}}})
	if err != nil {
		t.Fatal(err)
	}
	issues[4] = map[string]any{"number": 4, "state": "open", "title": "Dependent on epic fixture", "body": body}
	o := TransitionOptions{SessionID: "fixture-actual-requester", Mode: reviewpolicy.ModeTrusted}
	result, noop, err := c.TransitionWithCascades(ctx, "3", "review", o)
	if err != nil || noop || len(result.ParentStatusUpdates) != 2 {
		t.Fatalf("%+v noop=%v err=%v", result, noop, err)
	}
	for i, parent := range result.ParentStatusUpdates {
		if parent.ID != fmt.Sprintf("gh-%d", 2-i) || parent.Status != models.StatusInReview || parent.Minor || parent.ReviewRequestedBySession != o.SessionID || parent.ReviewerSession != "" || len(parent.Details.Reviews) != 0 {
			t.Fatalf("fictional review attribution: %+v", parent)
		}
		if parent.ID == "gh-2" && parent.ImplementerSession != "fixture-original-worker" {
			t.Fatal("epic implementer replaced")
		}
		if _, err := c.verifyReview(ctx, &parent, *parent.Details); err != nil {
			t.Fatal("auto review has invalid basis: ", err)
		}
	}
	o.SelfReview, o.Reason = true, "Fixture honest self-review of leaf"
	result, noop, err = c.TransitionWithCascades(ctx, "3", "approve", o)
	if err != nil || noop || len(result.ParentStatusUpdates) != 2 || len(result.AutoUnblocked) != 1 {
		t.Fatalf("%+v noop=%v err=%v", result, noop, err)
	}
	for _, parent := range result.ParentStatusUpdates {
		if parent.Status != models.StatusClosed || parent.ClosedBySession != o.SessionID || parent.ReviewerSession != "" || parent.ReviewedAt != nil || parent.Details.ReviewBasis != "" || len(parent.Details.Reviews) != 0 {
			t.Fatalf("automatic close falsely approved: %+v", parent)
		}
		last := parent.Details.Transitions[len(parent.Details.Transitions)-1]
		if last.SessionID != o.SessionID || last.Reason != "Auto-cascaded to closed (all children complete)" {
			t.Fatal("automatic closure provenance missing")
		}
	}
	if result.AutoUnblocked[0].ID != "gh-4" || result.AutoUnblocked[0].ImplementerSession != "" {
		t.Fatal("closed epic did not unblock its dependent")
	}
	if _, _, err := c.Transition(ctx, "1", "approve", o); err == nil {
		t.Fatal("automatic epic closure passed as independent approval")
	}
}

func TestParentCascadeStopsForIncompleteChildrenOrNonEpic(t *testing.T) {
	for _, nonEpic := range []bool{false, true} {
		t.Run(fmt.Sprint(nonEpic), func(t *testing.T) {
			c, issues, _ := parentFixture(t)
			if nonEpic {
				task := models.TypeTask
				if _, err := c.Update(context.Background(), "2", Changes{Type: &task}); err != nil {
					t.Fatal(err)
				}
			} else {
				body, err := encodeBody("Open sibling", metadata{Type: models.TypeTask, Priority: models.PriorityP2, Details: &IssueDetails{ParentID: "gh-2"}})
				if err != nil {
					t.Fatal(err)
				}
				issues[4] = map[string]any{"number": 4, "state": "open", "title": "Open sibling fixture", "body": body}
			}
			result, _, err := c.TransitionWithCascades(context.Background(), "3", "close", fixtureCloseOptions())
			if err != nil || len(result.ParentStatusUpdates) != 0 {
				t.Fatalf("%+v %v", result, err)
			}
			for _, id := range []string{"1", "2"} {
				p, err := c.Get(context.Background(), id)
				if err != nil || p.Status != models.StatusOpen {
					t.Fatalf("premature parent completion %+v %v", p, err)
				}
			}
		})
	}
}

func TestParentCascadeConflictAndPartialPermissionFailure(t *testing.T) {
	for _, denied := range []bool{false, true} {
		t.Run(fmt.Sprint(denied), func(t *testing.T) {
			c, issues, writes := parentFixture(t)
			base := c.run
			c.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
				if denied && slices.Contains(args, "PATCH") && args[5] == "repos/owner/repo/issues/1" {
					return nil, fmt.Errorf("fixture permission denied")
				}
				out, err := base(ctx, dir, payload, args...)
				if !denied && slices.Contains(args, "PATCH") && args[5] == "repos/owner/repo/issues/3" {
					issues[2]["title"] = "Concurrent changed epic fixture"
				}
				return out, err
			}
			before := *writes
			_, _, err := c.TransitionWithCascades(context.Background(), "3", "close", fixtureCloseOptions())
			want := 1
			if denied {
				want = 2
			}
			if err == nil || *writes != before+want || !strings.Contains(err.Error(), "saved for gh-3") {
				t.Fatalf("writes=%d err=%v", *writes-before, err)
			}
			if !denied {
				var conflict *ConflictError
				if !errors.As(err, &conflict) || !conflict.AfterWrite {
					t.Fatalf("not classified as conflict: %v", err)
				}
			}
			if denied && !strings.Contains(err.Error(), "gh-3, gh-2") {
				t.Fatal("partial changed parent not reported")
			}
		})
	}
}

func TestParentCascadeRejectsCyclesAndConcurrentNewSibling(t *testing.T) {
	for _, cycle := range []bool{true, false} {
		t.Run(fmt.Sprint(cycle), func(t *testing.T) {
			c, issues, writes := parentFixture(t)
			if cycle {
				body, err := encodeBody("Cyclic parent fixture", metadata{Type: models.TypeEpic, Priority: models.PriorityP2, Details: &IssueDetails{ParentID: "gh-3"}})
				if err != nil {
					t.Fatal(err)
				}
				issues[2]["body"] = body
			} else {
				base := c.run
				c.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
					out, err := base(ctx, dir, payload, args...)
					if slices.Contains(args, "PATCH") && args[5] == "repos/owner/repo/issues/3" {
						body, e := encodeBody("New sibling fixture", metadata{Type: models.TypeTask, Priority: models.PriorityP2, Details: &IssueDetails{ParentID: "gh-2"}})
						if e != nil {
							return nil, e
						}
						issues[4] = map[string]any{"number": 4, "state": "open", "title": "Concurrent new sibling fixture", "body": body}
					}
					return out, err
				}
			}
			before := *writes
			_, _, err := c.TransitionWithCascades(context.Background(), "3", "close", fixtureCloseOptions())
			want := 1
			if cycle {
				want = 0
			}
			if err == nil || *writes != before+want {
				t.Fatalf("invalid hierarchy changed: %v writes=%d", err, *writes-before)
			}
			if !cycle {
				var conflict *ConflictError
				if !errors.As(err, &conflict) || !conflict.AfterWrite {
					t.Fatalf("new sibling not detected: %v", err)
				}
			}
		})
	}
}

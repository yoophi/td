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

func reviewHierarchyFixture(t *testing.T) (*Client, map[int]map[string]any, *int) {
	t.Helper()
	c, issues, writes := hierarchyFixture(t)
	base := c.run
	c.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		if strings.HasSuffix(args[5], "/events?per_page=100") {
			return []byte(`[[]]`), nil
		}
		return base(ctx, dir, payload, args...)
	}
	minor := true
	if _, err := c.Update(context.Background(), "1", Changes{Minor: &minor}); err != nil {
		t.Fatal(err)
	}
	for id, parent := range map[string]string{"2": "gh-1", "3": "gh-2"} {
		p := parent
		if _, err := c.Update(context.Background(), id, Changes{ParentID: &p}); err != nil {
			t.Fatal(err)
		}
	}
	return c, issues, writes
}

func TestReviewCascadePreservesMajorChildrenAndSkipsBlocked(t *testing.T) {
	c, issues, _ := reviewHierarchyFixture(t)
	child, err := c.Get(context.Background(), "2")
	if err != nil {
		t.Fatal(err)
	}
	details, err := child.CopyDetails()
	if err != nil {
		t.Fatal(err)
	}
	details.Status = models.StatusInProgress
	details.ImplementerSession = "fixture-original-worker"
	if _, err := c.UpdateObserved(context.Background(), child, Changes{Details: &details}); err != nil {
		t.Fatal(err)
	}

	body, err := encodeBody("Blocked fixture", metadata{Type: models.TypeTask, Priority: models.PriorityP2, Details: &IssueDetails{Status: models.StatusBlocked, ParentID: "gh-1"}})
	if err != nil {
		t.Fatal(err)
	}
	issues[4] = map[string]any{"number": 4, "state": "open", "title": "Blocked child", "body": body}
	root, noop, err := c.TransitionWithCascades(context.Background(), "1", "review", TransitionOptions{SessionID: "fixture-worker", Mode: reviewpolicy.ModeTrusted})
	if err != nil || noop || len(root.CascadedReviews) != 2 {
		t.Fatalf("%+v noop=%v err=%v", root, noop, err)
	}
	for i, child := range root.CascadedReviews {
		if child.ID != fmt.Sprintf("gh-%d", i+2) || child.Status != models.StatusInReview || child.Minor || child.Details.ReviewHandoffID != "" || child.Details.ReviewBasis == "" || child.ReviewRequestedBySession != "fixture-worker" || child.ReviewerSession != "" || len(child.Details.Reviews) != 0 {
			t.Fatalf("wrong review attribution: %+v", child)
		}
		if child.ID == "gh-2" && child.ImplementerSession != "fixture-original-worker" {
			t.Fatal("cascade replaced implementer")
		}
		if child.Details.Transitions[len(child.Details.Transitions)-1].Reason != "Cascaded review from gh-1" {
			t.Fatal("missing cascade provenance")
		}
	}
	blocked, err := c.Get(context.Background(), "4")
	if err != nil || blocked.Status != models.StatusBlocked {
		t.Fatalf("%+v %v", blocked, err)
	}
}

func TestReviewCascadeReportsPartialFailure(t *testing.T) {
	c, _, writes := reviewHierarchyFixture(t)
	base := c.run
	c.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		if slices.Contains(args, "PATCH") && args[5] == "repos/owner/repo/issues/3" {
			return nil, fmt.Errorf("fixture permission denied")
		}
		return base(ctx, dir, payload, args...)
	}
	before := *writes
	_, _, err := c.TransitionWithCascades(context.Background(), "1", "review", TransitionOptions{SessionID: "fixture-worker", Mode: reviewpolicy.ModeTrusted})
	if err == nil || !strings.Contains(err.Error(), "saved for gh-1, gh-2") || !strings.Contains(err.Error(), "gh-3") || *writes != before+2 {
		t.Fatalf("writes=%d err=%v", *writes-before, err)
	}
	first, _ := c.Get(context.Background(), "2")
	last, _ := c.Get(context.Background(), "3")
	if first.Status != models.StatusInReview || last.Status != models.StatusOpen {
		t.Fatal("partial result not preserved")
	}
}

func TestReviewCascadeDetectsMembershipAndStaleRoot(t *testing.T) {
	c, issues, writes := reviewHierarchyFixture(t)
	observed, err := c.Get(context.Background(), "1")
	if err != nil {
		t.Fatal(err)
	}
	issues[1]["title"] = "Concurrent changed root"
	before := *writes
	_, _, err = c.TransitionObservedWithCascades(context.Background(), observed, "review", TransitionOptions{SessionID: "fixture-worker", Mode: reviewpolicy.ModeTrusted})
	var conflict *ConflictError
	if !errors.As(err, &conflict) || conflict.AfterWrite || *writes != before {
		t.Fatalf("stale root refreshed away: %v", err)
	}
	c, issues, writes = reviewHierarchyFixture(t)
	base := c.run
	lists := 0
	c.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		if strings.Contains(args[5], "?state=") && *writes > before {
			lists++
			if lists == 1 {
				body, e := encodeBody("New child", metadata{Type: models.TypeTask, Priority: models.PriorityP2, Details: &IssueDetails{ParentID: "gh-1"}})
				if e != nil {
					return nil, e
				}
				issues[4] = map[string]any{"number": 4, "state": "open", "title": "Concurrent new child", "body": body}
			}
		}
		return base(ctx, dir, payload, args...)
	}
	before = *writes
	_, _, err = c.TransitionWithCascades(context.Background(), "1", "review", TransitionOptions{SessionID: "fixture-worker", Mode: reviewpolicy.ModeTrusted})
	if err == nil || !strings.Contains(err.Error(), "membership changed") || *writes != before+1 {
		t.Fatalf("new child not detected: %v writes=%d", err, *writes-before)
	}
}

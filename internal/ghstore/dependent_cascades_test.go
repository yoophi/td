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

func dependentFixture(t *testing.T) (*Client, map[int]map[string]any, *int) {
	t.Helper()
	c, issues, writes := hierarchyFixture(t)
	for _, id := range []string{"2", "3"} {
		record, err := c.Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		d, err := record.CopyDetails()
		if err != nil {
			t.Fatal(err)
		}
		d.Status = models.StatusBlocked
		d.ImplementerSession = "fixture-prior-worker"
		d.Dependencies = []string{"gh-1"}
		if _, err := c.UpdateObserved(context.Background(), record, Changes{Details: &d}); err != nil {
			t.Fatal(err)
		}
	}
	return c, issues, writes
}

func fixtureCloseOptions() TransitionOptions {
	return TransitionOptions{SessionID: "fixture-actual-closer", Mode: reviewpolicy.ModeTrusted, AdminReason: "Fixture explicit administrative close"}
}

func TestCloseCascadeAllDependenciesAndClaimRelease(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprint(missing), func(t *testing.T) {
			c, _, _ := dependentFixture(t)
			third, err := c.Get(context.Background(), "3")
			if err != nil {
				t.Fatal(err)
			}
			d, err := third.CopyDetails()
			if err != nil {
				t.Fatal(err)
			}
			// gh-2 remains open after automatic unblocking, so gh-3 stays blocked.
			other := "gh-2"
			if missing {
				other = "gh-99"
			}
			d.Dependencies = append(d.Dependencies, other)
			if _, err := c.UpdateObserved(context.Background(), third, Changes{Details: &d}); err != nil {
				t.Fatal(err)
			}
			result, noop, err := c.TransitionWithCascades(context.Background(), "1", "close", fixtureCloseOptions())
			if err != nil || noop || result.Status != models.StatusClosed || len(result.AutoUnblocked) != 1 {
				t.Fatalf("%+v noop=%v err=%v", result, noop, err)
			}
			unblocked := result.AutoUnblocked[0]
			if unblocked.ID != "gh-2" || unblocked.Status != models.StatusOpen || unblocked.ImplementerSession != "" {
				t.Fatalf("wrong auto-unblock: %+v", unblocked)
			}
			transitions := unblocked.Details.Transitions
			if transitions[len(transitions)-1].Reason != "Auto-unblocked (dependency gh-1 closed)" || transitions[len(transitions)-1].SessionID != "fixture-actual-closer" {
				t.Fatal("missing actual actor/provenance")
			}
			history := unblocked.Details.Sessions
			if len(history) == 0 || history[len(history)-1].SessionID != "fixture-prior-worker" || history[len(history)-1].Action != models.ActionSessionUnstarted {
				t.Fatal("released claim history missing")
			}
			third, err = c.Get(context.Background(), "3")
			if err != nil || third.Status != models.StatusBlocked || third.ImplementerSession != "fixture-prior-worker" {
				t.Fatalf("unresolved dependent changed: %+v %v", third, err)
			}
		})
	}
}

func TestCloseCascadeReportsAppliedIDsOnFailure(t *testing.T) {
	c, _, writes := dependentFixture(t)
	base := c.run
	c.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		if slices.Contains(args, "PATCH") && args[5] == "repos/owner/repo/issues/3" {
			return nil, fmt.Errorf("fixture permission denied")
		}
		return base(ctx, dir, payload, args...)
	}
	before := *writes
	_, _, err := c.TransitionWithCascades(context.Background(), "1", "close", fixtureCloseOptions())
	if err == nil || !strings.Contains(err.Error(), "saved for gh-1, gh-2") || !strings.Contains(err.Error(), "gh-3") || *writes != before+2 {
		t.Fatalf("writes=%d err=%v", *writes-before, err)
	}
	second, _ := c.Get(context.Background(), "2")
	third, _ := c.Get(context.Background(), "3")
	if second.Status != models.StatusOpen || third.Status != models.StatusBlocked {
		t.Fatal("partial writes not retained")
	}
}

func TestCloseCascadeRejectsStaleRootAndChangedDependency(t *testing.T) {
	c, issues, writes := dependentFixture(t)
	observed, err := c.Get(context.Background(), "1")
	if err != nil {
		t.Fatal(err)
	}
	issues[1]["title"] = "Concurrent root fixture"
	before := *writes
	_, _, err = c.TransitionObservedWithCascades(context.Background(), observed, "close", fixtureCloseOptions())
	var conflict *ConflictError
	if !errors.As(err, &conflict) || conflict.AfterWrite || *writes != before {
		t.Fatalf("stale root overwritten: %v", err)
	}

	c, issues, writes = dependentFixture(t)
	base := c.run
	lists := 0
	c.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		if strings.Contains(args[5], "?state=") && *writes > before {
			lists++
			if lists == 1 {
				issues[2]["title"] = "Concurrent dependent fixture"
			}
		}
		return base(ctx, dir, payload, args...)
	}
	before = *writes
	_, _, err = c.TransitionWithCascades(context.Background(), "1", "close", fixtureCloseOptions())
	if !errors.As(err, &conflict) || !conflict.AfterWrite || *writes != before+1 || !strings.Contains(err.Error(), "saved for gh-1") {
		t.Fatalf("changed dependency not detected: %v writes=%d", err, *writes-before)
	}
}

func TestRecordOnlyApprovalDoesNotUnblock(t *testing.T) {
	f := newReviewFixture(t)
	ctx := context.Background()
	if _, _, err := f.client.TransitionWithCascades(ctx, "1", "review", TransitionOptions{SessionID: "fixture-implementer", Mode: reviewpolicy.ModeTrusted, Minor: true}); err != nil {
		t.Fatal(err)
	}
	result, _, err := f.client.TransitionWithCascades(ctx, "1", "approve", TransitionOptions{SessionID: "fixture-implementer", Mode: reviewpolicy.ModeTrusted, RecordOnly: true, SelfReview: true, Reason: "Fixture honest self-review"})
	if err != nil || result.Status != models.StatusInReview || len(result.AutoUnblocked) != 0 {
		t.Fatalf("%+v %v", result, err)
	}
}

func TestCloseCascadeDetectsNewDependentAndReopenedDependency(t *testing.T) {
	for _, newDependent := range []bool{true, false} {
		t.Run(fmt.Sprint(newDependent), func(t *testing.T) {
			c, issues, writes := dependentFixture(t)
			if !newDependent {
				third, err := c.Get(context.Background(), "3")
				if err != nil {
					t.Fatal(err)
				}
				d, err := third.CopyDetails()
				if err != nil {
					t.Fatal(err)
				}
				d.Status = models.StatusClosed
				native := models.StatusClosed
				if _, err := c.UpdateObserved(context.Background(), third, Changes{Details: &d, Status: &native}); err != nil {
					t.Fatal(err)
				}
				second, err := c.Get(context.Background(), "2")
				if err != nil {
					t.Fatal(err)
				}
				d, err = second.CopyDetails()
				if err != nil {
					t.Fatal(err)
				}
				d.Dependencies = []string{"gh-1", "gh-3"}
				if _, err := c.UpdateObserved(context.Background(), second, Changes{Details: &d}); err != nil {
					t.Fatal(err)
				}
			}
			before := *writes
			base := c.run
			lists := 0
			c.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
				if strings.Contains(args[5], "?state=") && *writes > before {
					lists++
					if lists == 1 {
						if newDependent {
							body, err := encodeBody("Fixture newly dependent issue", metadata{Type: models.TypeTask, Priority: models.PriorityP2, Details: &IssueDetails{Status: models.StatusBlocked, Dependencies: []string{"gh-1"}}})
							if err != nil {
								return nil, err
							}
							issues[4] = map[string]any{"number": 4, "state": "open", "title": "New dependent fixture", "body": body}
						} else {
							issues[3]["state"] = "open"
						}
					}
				}
				return base(ctx, dir, payload, args...)
			}
			_, _, err := c.TransitionWithCascades(context.Background(), "1", "close", fixtureCloseOptions())
			var conflict *ConflictError
			if !errors.As(err, &conflict) || !conflict.AfterWrite || *writes != before+1 || !strings.Contains(err.Error(), "saved for gh-1") {
				t.Fatalf("unsafe unblock after graph change: %v writes=%d", err, *writes-before)
			}
			second, err := c.Get(context.Background(), "2")
			if err != nil || second.Status != models.StatusBlocked {
				t.Fatalf("changed graph unblocked dependent: %+v %v", second, err)
			}
		})
	}
}

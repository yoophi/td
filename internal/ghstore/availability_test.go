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

func TestAvailabilityMirrorsReviewAndClosePolicyWithoutWrites(t *testing.T) {
	for _, tc := range []struct {
		name, session string
		mode          reviewpolicy.Mode
		minor         bool
		want          []string
	}{
		{"trusted own major", "fixture-worker", reviewpolicy.ModeTrusted, false, []string{"approve", "reject"}},
		{"strict own major", "fixture-worker", reviewpolicy.ModeStrict, false, []string{"reject"}},
		{"balanced independent", "fixture-independent", reviewpolicy.ModeBalanced, false, []string{"approve", "reject"}},
		{"delegated own major", "fixture-worker", reviewpolicy.ModeDelegated, false, []string{"reject"}},
		{"strict own minor", "fixture-worker", reviewpolicy.ModeStrict, true, []string{"approve", "reject", "close"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newReviewFixture(t)
			ctx := context.Background()
			if _, _, err := f.client.Transition(ctx, "1", "review", TransitionOptions{SessionID: "fixture-worker", Mode: reviewpolicy.ModeTrusted, Minor: tc.minor}); err != nil {
				t.Fatal(err)
			}
			observed, err := f.client.Get(ctx, "1")
			if err != nil {
				t.Fatal(err)
			}
			before := f.writes
			actions, err := f.client.AvailableTransitions(ctx, observed, tc.session, tc.mode)
			if err != nil || !slices.Equal(actions, tc.want) || f.writes != before {
				t.Fatalf("actions=%v writes=%d err=%v", actions, f.writes-before, err)
			}
			if strings.Contains(fmt.Sprint(f.issue["body"]), "availability probe") {
				t.Fatal("probe attribution persisted")
			}
		})
	}
}

func TestAvailabilityRecordedApprovalStalenessAndTransportErrors(t *testing.T) {
	f := newReviewFixture(t)
	ctx := context.Background()
	o := TransitionOptions{SessionID: "fixture-worker", Mode: reviewpolicy.ModeTrusted}
	if _, _, err := f.client.Transition(ctx, "1", "review", o); err != nil {
		t.Fatal(err)
	}
	o.Minor = false
	o.RecordOnly = true
	o.SelfReview = true
	o.Reason = "Fixture honest self-review"
	if _, _, err := f.client.Transition(ctx, "1", "approve", o); err != nil {
		t.Fatal(err)
	}
	observed, err := f.client.Get(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	actions, err := f.client.AvailableTransitions(ctx, observed, "fixture-other-closer", reviewpolicy.ModeTrusted)
	if err != nil || !slices.Equal(actions, []string{"approve", "reject", "close"}) {
		t.Fatalf("%v %v", actions, err)
	}
	f.issue["title"] = "Edited after approval fixture"
	observed, err = f.client.Get(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	actions, err = f.client.AvailableTransitions(ctx, observed, "fixture-other-closer", reviewpolicy.ModeTrusted)
	if err != nil || !slices.Equal(actions, []string{"reject"}) {
		t.Fatalf("stale recorded approval advertised: %v %v", actions, err)
	}
	base := f.client.run
	f.client.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		if strings.Contains(args[5], "/events?") {
			return nil, errors.New("fixture events permission denied")
		}
		return base(ctx, dir, payload, args...)
	}
	// Restore the reviewed content so verification reaches the events read.
	f.issue["title"] = "Fixture"
	observed, err = f.client.Get(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.client.AvailableTransitions(ctx, observed, "fixture-worker", reviewpolicy.ModeTrusted); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("transport error hidden: %v", err)
	}
}

func TestAvailabilityBasicStatesAndConcurrentObservation(t *testing.T) {
	for _, tc := range []struct {
		state models.Status
		want  []string
	}{
		{models.StatusOpen, []string{"start", "review", "block", "close"}},
		{models.StatusInProgress, []string{"review", "block"}},
		{models.StatusBlocked, []string{"unblock"}},
		{models.StatusClosed, []string{"reopen"}},
	} {
		t.Run(string(tc.state), func(t *testing.T) {
			f := newReviewFixture(t)
			ctx := context.Background()
			observed, err := f.client.Get(ctx, "1")
			if err != nil {
				t.Fatal(err)
			}
			d, err := observed.CopyDetails()
			if err != nil {
				t.Fatal(err)
			}
			d.Status = tc.state
			d.CreatorSession = "fixture-worker"
			if tc.state != models.StatusOpen {
				d.ImplementerSession = "fixture-worker"
			}
			native := nativeStatus(tc.state)
			observed, err = f.client.UpdateObserved(ctx, observed, Changes{Details: &d, Status: &native})
			if err != nil {
				t.Fatal(err)
			}
			before := f.writes
			actions, err := f.client.AvailableTransitions(ctx, observed, "fixture-worker", reviewpolicy.ModeTrusted)
			if err != nil || !slices.Equal(actions, tc.want) || f.writes != before {
				t.Fatalf("%v %v", actions, err)
			}
			f.issue["title"] = "Concurrent changed fixture"
			_, err = f.client.AvailableTransitions(ctx, observed, "fixture-worker", reviewpolicy.ModeTrusted)
			var conflict *ConflictError
			if !errors.As(err, &conflict) || conflict.AfterWrite || f.writes != before {
				t.Fatalf("stale read accepted: %v", err)
			}
		})
	}
}

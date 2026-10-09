package ghstore

import (
	"context"
	"errors"
	"github.com/marcus/td/internal/reviewpolicy"
	"strings"
	"testing"
)

func TestMonitorReviewFactsApprovalStalenessAndRetainedParticipation(t *testing.T) {
	f := newReviewFixture(t)
	ctx := context.Background()
	f.transition(t, "start", TransitionOptions{SessionID: "fixture-worker", Mode: reviewpolicy.ModeTrusted})
	f.transition(t, "review", TransitionOptions{SessionID: "fixture-worker", Mode: reviewpolicy.ModeTrusted})
	f.transition(t, "approve", TransitionOptions{SessionID: "fixture-worker", Mode: reviewpolicy.ModeTrusted, RecordOnly: true, SelfReview: true, Reason: "Fixture self-review"})
	observed, err := f.client.Get(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	before := f.writes
	facts, err := f.client.ObserveMonitorReview(ctx, observed, "fixture-worker")
	if err != nil || !facts.Fresh || !facts.ActiveApproval || !facts.ImplementationInvolved || !facts.AnyInvolved || f.writes != before {
		t.Fatalf("%+v %v writes=%d", facts, err, f.writes-before)
	}
	other, err := f.client.ObserveMonitorReview(ctx, observed, "fixture-independent")
	if err != nil || !other.Fresh || !other.ActiveApproval || other.ImplementationInvolved || other.AnyInvolved {
		t.Fatalf("independent: %+v %v", other, err)
	}
	f.issue["title"] = "Edited since approval"
	observed, err = f.client.Get(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	facts, err = f.client.ObserveMonitorReview(ctx, observed, "fixture-worker")
	if err != nil || facts.Fresh || facts.ActiveApproval || !facts.ImplementationInvolved {
		t.Fatalf("stale: %+v %v", facts, err)
	}
	f.issue["title"] = "Fixture"
	f.nativeState("closed")
	f.nativeState("open")
	observed, err = f.client.Get(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	facts, err = f.client.ObserveMonitorReview(ctx, observed, "fixture-worker")
	if err != nil || facts.Fresh || facts.ActiveApproval {
		t.Fatalf("native state history revived approval: %+v %v", facts, err)
	}
	if f.writes != before {
		t.Fatal("read changed approval")
	}
}
func TestMonitorReviewFactsPropagatesTransportConflictAndInvalidObservation(t *testing.T) {
	f := newReviewFixture(t)
	ctx := context.Background()
	f.transition(t, "review", TransitionOptions{SessionID: "fixture-worker", Mode: reviewpolicy.ModeTrusted})
	observed, err := f.client.Get(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	base := f.client.run
	f.client.run = func(ctx context.Context, dir string, input []byte, args ...string) ([]byte, error) {
		if strings.Contains(args[5], "/events?") {
			return nil, errors.New("events permission denied")
		}
		return base(ctx, dir, input, args...)
	}
	if facts, err := f.client.ObserveMonitorReview(ctx, observed, "fixture-independent"); err == nil || facts != nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("hidden read failure: %+v %v", facts, err)
	}
	f.client.run = base
	f.issue["title"] = "Concurrent change"
	if facts, err := f.client.ObserveMonitorReview(ctx, observed, "fixture-independent"); err == nil || facts != nil {
		t.Fatal("stale observation returned actionable facts")
	}
	copy := *observed
	copy.repository = "wrong/repo"
	for _, record := range []*Record{nil, &copy} {
		if _, err := f.client.ObserveMonitorReview(ctx, record, "fixture-worker"); err == nil {
			t.Fatal("untrusted observation accepted")
		}
	}
	if _, err := f.client.ObserveMonitorReview(ctx, observed, " "); err == nil {
		t.Fatal("blank actor accepted")
	}
}

package ghstore

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestReleaseObservedClaimPreservesNewOwnerAndNewActivity(t *testing.T) {
	for _, kind := range []string{"unchanged", "activity", "new-owner"} {
		t.Run(kind, func(t *testing.T) {
			f := newReviewFixture(t)
			observed := f.transition(t, "start", TransitionOptions{SessionID: "old-worker"})
			switch kind {
			case "activity":
				f.issue["updated_at"] = time.Now().UTC().Format(time.RFC3339)
			case "new-owner":
				f.transition(t, "unstart", TransitionOptions{SessionID: "supervisor"})
				f.transition(t, "start", TransitionOptions{SessionID: "new-worker"})
			}
			before := f.writes
			r, err := f.client.ReleaseObservedClaim(context.Background(), observed, TransitionOptions{SessionID: "supervisor", Reason: "Release observed fixture claim"})
			if kind == "unchanged" {
				if err != nil || r.ImplementerSession != "" || f.writes != before+1 {
					t.Fatalf("%+v %v", r, err)
				}
				return
			}
			var conflict *ConflictError
			if !errors.As(err, &conflict) || conflict.AfterWrite || f.writes != before {
				t.Fatalf("stale release wrote: %v", err)
			}
		})
	}
}

package ghstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

func TestNativeEventObservationBoundaryIsolationAndCancellation(t *testing.T) {
	calls := 0
	events := []nativeStateEvent{{ID: 1, Event: "closed"}}
	client := &Client{repo: "owner/repo", run: func(context.Context, string, []byte, ...string) ([]byte, error) {
		calls++
		return json.Marshal([][]nativeStateEvent{events})
	}}
	ctx := withNativeEventObservation(context.Background())
	first, err := client.readStateEvents(ctx, "gh-1")
	if err != nil {
		t.Fatal(err)
	}
	first[0].ID = 99 // A consumer cannot mutate another proof's observation.
	second, err := client.readStateEvents(ctx, "gh-1")
	if err != nil || calls != 1 || second[0].ID != 1 {
		t.Fatal(calls, second, err)
	}
	events = append(events, nativeStateEvent{ID: 2, Event: "reopened"})
	next, err := client.readStateEvents(withNativeEventObservation(ctx), "gh-1")
	if err != nil || calls != 2 || len(next) != 2 {
		t.Fatal("next fence reused old history", calls, next, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := client.readStateEvents(canceled, "gh-1"); !errors.Is(err, context.Canceled) || calls != 2 {
		t.Fatal("cached proof ignored cancellation", calls, err)
	}
	other := &Client{repo: "owner/repo", run: func(context.Context, string, []byte, ...string) ([]byte, error) { return []byte(`[[]]`), nil }}
	if rows, err := other.readStateEvents(ctx, "gh-1"); err != nil || rows == nil || len(rows) != 0 {
		t.Fatal("observation leaked across clients or changed empty-array semantics", rows, err)
	}
}

func TestNativeEventObservationFailureMakesOneAttemptAndRetainsCause(t *testing.T) {
	calls := 0
	failure := fmt.Errorf("fixture API denied")
	client := &Client{repo: "owner/repo", run: func(context.Context, string, []byte, ...string) ([]byte, error) { calls++; return nil, failure }}
	if _, err := client.readStateEvents(withNativeEventObservation(context.Background()), "gh-1"); !errors.Is(err, failure) || calls != 1 {
		t.Fatal(calls, err)
	}
}

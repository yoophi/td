package ghstore

import (
	"context"
	"errors"
	"testing"
)

func TestReadObservedRejectsStaleForeignInvalidAndCancelledWithoutWrites(t *testing.T) {
	f := newReviewFixture(t)
	observed, err := f.client.Get(context.Background(), "gh-1")
	if err != nil {
		t.Fatal(err)
	}
	// Public display fields cannot substitute for the private observation.
	observed.Title = "public field changed"
	current, err := f.client.ReadObserved(context.Background(), observed)
	if err != nil || current.Title == observed.Title || f.writes != 0 || f.posts != 0 {
		t.Fatal("read did not preserve original observation", current, err)
	}
	for _, invalid := range []*Record{nil, {}, func() *Record { r := *observed; r.repository = "foreign/repo"; return &r }(), func() *Record { r := *observed; r.Number++; return &r }()} {
		if _, err := f.client.ReadObserved(context.Background(), invalid); err == nil {
			t.Fatal("untrusted observation accepted")
		}
	}
	f.issue["title"] = "Peer changed title"
	var conflict *ConflictError
	if _, err := f.client.ReadObserved(context.Background(), observed); !errors.As(err, &conflict) {
		t.Fatal("stale read adopted newer revision", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.client.ReadObserved(ctx, observed); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled read ran", err)
	}
	if f.writes != 0 || f.posts != 0 {
		t.Fatal("read wrote to GitHub")
	}
}

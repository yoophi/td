package ghstore

import (
	"context"
	"slices"
	"sync"
)

type nativeEventObservationKey struct{}
type nativeEventObservationTarget struct {
	client *Client
	id     string
}

// This memo belongs only to one read-only validation boundary. It is never a
// process/TTL cache and never crosses an intent, target write or fresh fence.
// Each proof is still compared independently against the observed events.
type nativeEventObservation struct {
	mu     sync.Mutex
	events map[nativeEventObservationTarget][]nativeStateEvent
}

func withNativeEventObservation(ctx context.Context) context.Context {
	return context.WithValue(ctx, nativeEventObservationKey{}, &nativeEventObservation{events: make(map[nativeEventObservationTarget][]nativeStateEvent)})
}

func ensureNativeEventObservation(ctx context.Context) context.Context {
	if _, ok := ctx.Value(nativeEventObservationKey{}).(*nativeEventObservation); ok {
		return ctx
	}
	return withNativeEventObservation(ctx)
}

func (o *nativeEventObservation) read(ctx context.Context, c *Client, id string) ([]nativeStateEvent, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := nativeEventObservationTarget{c, id}
	if events, ok := o.events[key]; ok {
		return slices.Clone(events), nil
	}
	events, err := c.readStateEventsUncached(ctx, id)
	if err != nil {
		return nil, err
	}
	o.events[key] = slices.Clone(events)
	return slices.Clone(events), nil
}

package serve

import (
	"context"
	"errors"
	"github.com/marcus/td/internal/ghstore"
	"net/http"
	"os"
	"sync"
	"time"
)

type githubChangeTokenClient interface {
	ChangeToken(context.Context) (string, error)
}
type githubEventHub struct {
	read     func(context.Context) (string, error)
	interval time.Duration
	mu       sync.Mutex
	token    string
	failure  error
	clients  map[chan SSEEvent]struct{}
	cancel   context.CancelFunc
	done     chan struct{}
	stopped  bool
}

func newGitHubEventHub(read func(context.Context) (string, error), interval time.Duration) *githubEventHub {
	return &githubEventHub{read: read, interval: interval, clients: map[chan SSEEvent]struct{}{}}
}
func (h *githubEventHub) start(parent context.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stopped || h.done != nil {
		return
	}
	ctx, cancel := context.WithCancel(parent)
	h.cancel = cancel
	h.done = make(chan struct{})
	go h.run(ctx)
}
func (h *githubEventHub) stop() {
	h.mu.Lock()
	h.stopped = true
	if h.cancel != nil {
		h.cancel()
	}
	done := h.done
	for ch := range h.clients {
		close(ch)
		delete(h.clients, ch)
	}
	h.mu.Unlock()
	if done != nil {
		<-done
	}
}
func (h *githubEventHub) unregister(ch chan SSEEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.clients[ch]; ok {
		delete(h.clients, ch)
		close(ch)
	}
}
func (h *githubEventHub) register() (chan SSEEvent, string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stopped {
		return nil, "", errors.New("event stream is stopped")
	}
	if h.failure != nil {
		return nil, "", h.failure
	}
	if h.token == "" {
		return nil, "", errors.New("event stream initial observation is pending")
	}
	ch := make(chan SSEEvent, 16)
	h.clients[ch] = struct{}{}
	return ch, h.token, nil
}
func (h *githubEventHub) publishLocked(event SSEEvent) {
	for ch := range h.clients {
		select {
		case ch <- event:
		default:
			close(ch)
			delete(h.clients, ch)
		}
	}
}
func (h *githubEventHub) observe(ctx context.Context) bool {
	token, err := h.read(ctx)
	if ctx.Err() != nil {
		return false
	}
	if err == nil && token == "" {
		err = errors.New("GitHub returned an empty change token")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stopped {
		return false
	}
	previousFailure := h.failure
	h.failure = err
	if err != nil {
		payload := map[string]any{"code": "store_error", "message": err.Error()}
		var limit *ghstore.RateLimitError
		if errors.As(err, &limit) {
			payload["code"] = "rate_limited"
			payload["retry_after"] = int(limit.MinimumWait().Seconds())
		}
		h.publishLocked(SSEEvent{ID: h.token, Event: "store_error", Data: marshalJSON(payload)})
		return false
	}
	previous := h.token
	h.token = token
	if previous == token && previousFailure == nil {
		return true
	}
	// Recovery refreshes subscribers even when content is unchanged: their
	// intervening fetches may have failed during the unavailable observation.
	h.publishLocked(SSEEvent{ID: token, Event: "refresh", Data: marshalJSON(refreshData{ChangeToken: token, Timestamp: time.Now().UTC().Format(time.RFC3339)})})
	return true
}
func (h *githubEventHub) run(ctx context.Context) {
	defer close(h.done)
	defer func() {
		h.mu.Lock()
		h.stopped = true
		for ch := range h.clients {
			close(ch)
			delete(h.clients, ch)
		}
		h.mu.Unlock()
	}()
	delay := h.interval
	if !h.observe(ctx) {
		delay = h.failureDelay(delay, false)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	ping := time.NewTicker(30 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if h.observe(ctx) {
				delay = h.interval
			} else {
				delay = h.failureDelay(delay, true)
			}
			timer.Reset(delay)
		case <-ping.C:
			h.mu.Lock()
			if h.token != "" && h.failure == nil {
				h.publishLocked(SSEEvent{ID: h.token, Event: "ping", Data: marshalJSON(pingData{ChangeToken: h.token})})
			}
			h.mu.Unlock()
		}
	}
}
func (s *Server) EnableGitHubEvents(store *GitHubReadStore) {
	interval := s.config.PollInterval
	// Keep the conservative event interval until coordinated polling (#50).
	// Complete tokens now use repository-wide issue/comment pages.
	if interval < 5*time.Minute {
		interval = 5 * time.Minute
	}
	s.githubEvents = newGitHubEventHub(func(ctx context.Context) (string, error) {
		if os.Getenv("TD_GH_DEBUG") == "1" {
			ctx, _ = ghstore.WithAPICost(ctx)
			defer ghstore.LogAPICostContext(ctx)
		}
		client, err := store.open(ctx)
		if err != nil {
			return "", err
		}
		provider, ok := client.(githubChangeTokenClient)
		if !ok {
			return "", errors.New("GitHub change-token provider unavailable")
		}
		return provider.ChangeToken(ctx)
	}, interval)
	s.githubCapabilities = append(s.githubCapabilities, "events")
	s.githubEndpoints = append(s.githubEndpoints, "GET /v1/events")
	s.mux.HandleFunc("GET /v1/events", s.handleGitHubEvents)
}
func (s *Server) handleGitHubEvents(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		WriteError(w, ErrValidation, "event stream does not support query parameters", 400)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		WriteError(w, ErrInternal, "streaming not supported", 500)
		return
	}
	ch, token, err := s.githubEvents.register()
	if err != nil {
		if !writeGitHubRateLimit(w, err) {
			WriteError(w, "store_error", err.Error(), 503)
		}
		return
	}
	defer s.githubEvents.unregister(ch)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	event := SSEEvent{ID: token, Event: "ping", Data: marshalJSON(pingData{ChangeToken: token})}
	if last := r.Header.Get("Last-Event-ID"); last != "" && last != token {
		event.Event = "refresh"
		event.Data = marshalJSON(refreshData{ChangeToken: token, Timestamp: time.Now().UTC().Format(time.RFC3339)})
	}
	writeSSEEvent(w, flusher, event)
	for {
		select {
		case <-r.Context().Done():
			return
		case event, ok := <-ch:
			if !ok {
				return
			}
			writeSSEEvent(w, flusher, event)
		}
	}
}

// Failure backoff never shortens a user's longer configured poll interval.
func (h *githubEventHub) failureDelay(previous time.Duration, grow bool) time.Duration {
	delay := previous
	ceiling := max(h.interval, 5*time.Minute)
	if grow {
		if delay >= ceiling/2 {
			delay = ceiling
		} else {
			delay *= 2
		}
	}
	h.mu.Lock()
	failure := h.failure
	h.mu.Unlock()
	var limit *ghstore.RateLimitError
	delay = max(h.interval, min(delay, ceiling))
	if errors.As(failure, &limit) {
		delay = max(delay, limit.MinimumWait())
	}
	return delay
}

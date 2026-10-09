package ghstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Cooldowns are process-local and partitioned by repository and actual credential.
// Only credential digests survive; a cache opt-out must not disable rate protection.
type apiCooldownRegistry struct {
	mu     sync.Mutex
	limits map[string]apiCooldownObservation
}
type apiCooldownObservation struct {
	err   error
	until time.Time
}
type apiCooldownScope struct {
	registry *apiCooldownRegistry
	key      string
}

var processAPICooldowns = &apiCooldownRegistry{limits: map[string]apiCooldownObservation{}}

func newAPICooldown(repo string, token []byte) *apiCooldownScope {
	credential := strings.TrimSpace(string(token))
	if credential == "" {
		return nil
	}
	return &apiCooldownScope{registry: processAPICooldowns, key: cacheHash("td-cooldown-v1\ngithub.com\n" + strings.ToLower(repo) + "\n" + credential)}
}
func (s *apiCooldownScope) check(ctx context.Context, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil {
		return nil
	}
	s.registry.mu.Lock()
	observation, ok := s.registry.limits[s.key]
	if ok && !now.Before(observation.until) {
		delete(s.registry.limits, s.key)
		ok = false
	}
	s.registry.mu.Unlock()
	if !ok {
		return nil
	}
	return fmt.Errorf("%w; no GitHub request was attempted during the shared cooldown", observation.err)
}
func (s *apiCooldownScope) observe(err error, now time.Time) {
	if s == nil {
		return
	}
	var limit *RateLimitError
	if !errors.As(err, &limit) {
		return
	}
	until := limit.RetryAt
	if !until.After(now) {
		until = now.Add(limit.waitAt(now))
	}
	s.registry.mu.Lock()
	defer s.registry.mu.Unlock()
	// Expired observations need not retain diagnostics or credential digests.
	for key, observation := range s.registry.limits {
		if !now.Before(observation.until) {
			delete(s.registry.limits, key)
		}
	}
	if previous, ok := s.registry.limits[s.key]; !ok || until.After(previous.until) {
		s.registry.limits[s.key] = apiCooldownObservation{err: err, until: until}
	}
}

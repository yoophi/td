package ghstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSharedCooldownBlocksOtherClientsAndWritesWithoutRetry(t *testing.T) {
	t.Setenv("TD_GH_CACHE", "off")
	token := []byte(t.Name())
	first := newAPICooldown("Owner/Repo", token)
	second := newAPICooldown("owner/repo", token)
	cause := errors.New("original request-id HTTP 429")
	limit := &RateLimitError{Cause: cause, RetryAt: time.Now().Add(time.Hour), WaitSource: "retry-after"}
	var calls atomic.Int32
	run := func(context.Context, string, []byte, ...string) ([]byte, error) {
		calls.Add(1)
		return nil, limit
	}
	c := &Client{repo: "owner/repo", cooldown: first, run: run}
	_, err := c.request(context.Background(), "PATCH", "/issues/1", nil, false)
	if !errors.Is(err, cause) || calls.Load() != 1 {
		t.Fatalf("initial attempt: %v / %d", err, calls.Load())
	}
	other := &Client{repo: "owner/repo", cooldown: second, run: run}
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			for _, method := range []string{"GET", "PATCH"} {
				_, err := other.request(context.Background(), method, "/issues/1", nil, false)
				var observed *RateLimitError
				if !errors.As(err, &observed) || observed.RetryAt != limit.RetryAt || !errors.Is(err, cause) || !strings.Contains(err.Error(), "no GitHub request was attempted") {
					t.Errorf("lost cooldown diagnostic: %v", err)
				}
			}
		})
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("retried %d calls", calls.Load())
	}
	if err := newAPICooldown("owner/other", token).check(context.Background(), time.Now()); err != nil {
		t.Fatal("repository scope mixed", err)
	}
	if err := newAPICooldown("owner/repo", []byte("other-"+t.Name())).check(context.Background(), time.Now()); err != nil {
		t.Fatal("credential scope mixed", err)
	}
}

func TestCooldownDeadlinesCancellationAndPermissionFailures(t *testing.T) {
	now := time.Now()
	registry := &apiCooldownRegistry{limits: map[string]apiCooldownObservation{}}
	scope := &apiCooldownScope{registry: registry, key: "isolated"}
	scope.observe(errors.New("HTTP 403 permission denied"), now)
	if err := scope.check(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	cause := errors.New("original HTTP 429 without headers")
	scope.observe(&RateLimitError{Cause: cause}, now)
	if err := scope.check(context.Background(), now.Add(59*time.Second)); !errors.Is(err, cause) {
		t.Fatal("fallback deadline lost", err)
	}
	scope.observe(&RateLimitError{Cause: errors.New("shorter"), RetryAt: now.Add(10 * time.Second)}, now)
	if err := scope.check(context.Background(), now.Add(59*time.Second)); !errors.Is(err, cause) {
		t.Fatal("shortened active deadline", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := scope.check(ctx, now); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := scope.check(context.Background(), now.Add(time.Minute)); err != nil {
		t.Fatal("deadline remained active", err)
	}
	if len(registry.limits) != 0 {
		t.Fatal("expired diagnostics retained")
	}
}

func TestRepositoryPreflightSharesCooldownWithSubsequentOpen(t *testing.T) {
	cause := errors.New("preflight original request-id HTTP 403 rate limit exceeded")
	limit := &RateLimitError{Cause: cause, RetryAt: time.Now().Add(time.Hour), WaitSource: "x-ratelimit-reset"}
	apiCalls := 0
	authCalls := 0
	run := func(_ context.Context, _ string, name string, args ...string) ([]byte, error) {
		switch name + " " + args[0] {
		case "git rev-parse":
			return []byte("true"), nil
		case "git remote":
			return []byte("https://github.com/cooldown-test/repo"), nil
		case "gh auth":
			authCalls++
			return []byte(t.Name()), nil
		case "gh api":
			apiCalls++
			return nil, limit
		default:
			return nil, fmt.Errorf("unexpected command")
		}
	}
	for range 2 {
		_, err := resolveRepository(context.Background(), "/fixture", "origin", run)
		if !errors.Is(err, cause) || !strings.Contains(err.Error(), "original request-id") {
			t.Fatal(err)
		}
	}
	if apiCalls != 1 || authCalls != 2 {
		t.Fatalf("preflight=%d auth=%d", apiCalls, authCalls)
	}
	// Auth is still read locally to detect a changed identity. A changed token
	// must not inherit another credential's remote failure.
	freshRun := func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
		if name == "gh" && args[0] == "auth" {
			return []byte("changed-" + t.Name()), nil
		}
		if name == "gh" && args[0] == "api" {
			apiCalls++
			return []byte(`{"full_name":"cooldown-test/repo","has_issues":true}`), nil
		}
		return run(ctx, dir, name, args...)
	}
	if _, err := resolveRepository(context.Background(), "/fixture", "origin", freshRun); err != nil {
		t.Fatal(err)
	}
	if apiCalls != 2 {
		t.Fatalf("new credential did not probe: %d", apiCalls)
	}
}

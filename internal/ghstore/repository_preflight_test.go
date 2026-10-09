package ghstore

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRepositoryPreflightCoalescesOnlyOverlappingSameIdentity(t *testing.T) {
	scope := newAPICooldown("owner/repo", []byte(t.Name()))
	var calls atomic.Int32
	entered := make(chan struct{}, 10)
	release := make(chan struct{})
	run := func(ctx context.Context, _ string, _ string, _ ...string) ([]byte, error) {
		calls.Add(1)
		entered <- struct{}{}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
			return []byte(`{"full_name":"owner/repo","has_issues":true}`), nil
		}
	}
	leader := make(chan error, 1)
	go func() {
		_, err := repositoryPreflight(context.Background(), "/first", "owner/repo", run, scope)
		leader <- err
	}()
	<-entered
	// Register a same-key flight directly, with a function that must not run.
	// This synchronously establishes overlap without sleep or scheduling guesses.
	follower := repositoryPreflights.DoChan(scope.key, func() (any, error) { t.Error("same identity repeated preflight"); return nil, errors.New("duplicate") })
	otherScope := newAPICooldown("owner/repo", []byte("other-"+t.Name()))
	other := make(chan error, 1)
	go func() {
		_, err := repositoryPreflight(context.Background(), "/other", "owner/repo", run, otherScope)
		other <- err
	}()
	<-entered
	close(release)
	if err := <-leader; err != nil {
		t.Fatal(err)
	}
	if err := <-other; err != nil {
		t.Fatal(err)
	}
	result := <-follower
	if result.Err != nil || !result.Shared || len(result.Val.([]byte)) == 0 || calls.Load() != 2 {
		t.Fatalf("overlap result=%+v calls=%d", result, calls.Load())
	}
	if _, err := repositoryPreflight(context.Background(), "/later", "owner/repo", run, scope); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatal("completed permission check reused")
	}
}

func TestRepositoryPreflightCanceledWaiterDoesNotCancelLeader(t *testing.T) {
	scope := newAPICooldown("owner/repo", []byte(t.Name()))
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	run := func(ctx context.Context, _ string, _ string, _ ...string) ([]byte, error) {
		calls.Add(1)
		close(entered)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
			return []byte("success"), nil
		}
	}
	done := make(chan error, 1)
	go func() {
		_, err := repositoryPreflight(context.Background(), "/leader", "owner/repo", run, scope)
		done <- err
	}()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := repositoryPreflight(ctx, "/waiter", "owner/repo", run, scope)
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal("waiter canceled leader", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("duplicate attempt: %d", calls.Load())
	}
}

func TestCoalescedPreflightStillChecksLocalRemoteAndAuth(t *testing.T) {
	const n = 12
	var gitCalls, authCalls, apiCalls atomic.Int32
	allAuth := make(chan struct{})
	run := func(ctx context.Context, _ string, name string, args ...string) ([]byte, error) {
		if name == "git" {
			gitCalls.Add(1)
			if args[0] == "rev-parse" {
				return []byte("true"), nil
			}
			return []byte("https://github.com/local-checked/repo"), nil
		}
		if args[0] == "auth" {
			if authCalls.Add(1) == n {
				close(allAuth)
			}
			return []byte(t.Name()), nil
		}
		apiCalls.Add(1)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-allAuth:
		}
		// Keep the remote operation in flight while all locally validated callers
		// enter the shared request; this models network latency, not a retry wait.
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
		return []byte(`{"full_name":"local-checked/repo","has_issues":true}`), nil
	}
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			cfg, err := resolveRepository(context.Background(), "/fixture", "origin", run)
			if err != nil || cfg.Repo != "local-checked/repo" {
				t.Errorf("local check failed: %v", err)
			}
		})
	}
	wg.Wait()
	if gitCalls.Load() != 2*n || authCalls.Load() != n || apiCalls.Load() != 1 {
		t.Fatalf("git=%d auth=%d api=%d", gitCalls.Load(), authCalls.Load(), apiCalls.Load())
	}
}

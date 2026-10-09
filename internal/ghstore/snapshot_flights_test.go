package ghstore

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestUncachedSnapshotsCoalesceConcurrentConsumersWithoutRetainingData(t *testing.T) {
	for _, mode := range []string{"off", "unavailable"} {
		t.Run(mode, func(t *testing.T) {
			var cache *snapshotCache
			if mode == "off" {
				t.Setenv("TD_GH_CACHE", "off")
			} else {
				t.Setenv("TD_GH_CACHE", "on")
				root := filepath.Join(t.TempDir(), "td", "gh-issue", "unavailable")
				if err := os.MkdirAll(filepath.Dir(root), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(root, []byte("file"), 0600); err != nil {
					t.Fatal(err)
				}
				cache = &snapshotCache{root: root, repo: "owner/repo", credential: cacheHash(t.Name())}
			}
			var calls atomic.Int32
			started, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			run := func(ctx context.Context, _ string, _ []byte, args ...string) ([]byte, error) {
				calls.Add(1)
				once.Do(func() { close(started) })
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-release:
				}
				return json.Marshal([][]apiIssue{{{Number: 1, State: "open", Title: "remote"}}})
			}
			const n = 12
			scope := newAPICooldown("owner/repo", []byte(t.Name()))
			var wg sync.WaitGroup
			ready := sync.WaitGroup{}
			ready.Add(n)
			for range n {
				wg.Go(func() {
					c := &Client{repo: "owner/repo", cooldown: scope, cache: cache, run: run}
					ready.Done()
					s, err := c.ReadSnapshot(context.Background(), false)
					if err != nil || len(s.Issues(true)) != 1 {
						t.Errorf("consumer failed: %v", err)
					}
				})
			}
			ready.Wait()
			<-started
			// Model network latency so concurrently scheduled consumers join the flight.
			time.Sleep(100 * time.Millisecond)
			close(release)
			wg.Wait()
			if calls.Load() != 1 {
				t.Fatalf("concurrent uncached sweeps=%d", calls.Load())
			}
			c := &Client{repo: "owner/repo", cooldown: scope, cache: cache, run: run}
			if _, err := c.ReadSnapshot(context.Background(), false); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 2 {
				t.Fatal("completed result retained despite cache opt-out")
			}

		})
	}
}

func TestUncachedWriteFenceSeparatesNewReadersAcrossCredentials(t *testing.T) {
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	scope := newAPICooldown("owner/repo", []byte(t.Name()+"-reader"))
	run := func(ctx context.Context, _ string, _ []byte, args ...string) ([]byte, error) {
		call := calls.Add(1)
		title := "new"
		if call == 1 {
			close(started)
			title = "old"
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-release:
			}
		}
		return json.Marshal([][]apiIssue{{{Number: 1, State: "open", Title: title}}})
	}
	reader := &Client{repo: "owner/repo", cooldown: scope, run: run}
	old := make(chan *Snapshot, 1)
	go func() {
		s, err := reader.ReadSnapshot(context.Background(), false)
		if err != nil {
			t.Error(err)
		}
		old <- s
	}()
	<-started
	writer := &Client{repo: "OWNER/REPO", cooldown: newAPICooldown("owner/repo", []byte(t.Name()+"-writer")), run: func(context.Context, string, []byte, ...string) ([]byte, error) { return []byte(`{}`), nil }}
	before := reader.snapshotReadGeneration(false)
	if _, err := writer.request(context.Background(), "PATCH", "/issues/1", nil, false); err != nil {
		t.Fatal(err)
	}
	if reader.snapshotReadGeneration(false) != before+2 {
		t.Fatal("write did not fence both boundaries across credentials")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	current, err := reader.ReadSnapshot(ctx, false)
	if err != nil || current.Issues(true)[0].Title != "new" {
		t.Fatal("post-write read joined old flight", err)
	}
	close(release)
	previous := <-old
	if previous == nil || previous.Issues(true)[0].Title != "old" || calls.Load() != 2 {
		t.Fatal("older observation lost or duplicate sweep")
	}
}

func TestCacheDisabledWriterInvalidatesPersistentReader(t *testing.T) {
	c, _ := cachedSnapshotFixture(t)
	if _, err := c.ReadSnapshot(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	writer := &Client{repo: c.repo, invalidationCache: c.cache, run: func(context.Context, string, []byte, ...string) ([]byte, error) {
		return nil, errors.New("uncertain failure")
	}}
	if _, err := writer.request(context.Background(), "PATCH", "/issues/1", nil, false); err == nil {
		t.Fatal("lost write failure")
	}
	var generation string
	if err := c.cache.withLock(context.Background(), func() error { var err error; generation, err = c.cache.generation(); return err }); err != nil {
		t.Fatal(err)
	}
	if s, err := c.cache.load(c, true, generation, time.Now()); err != nil || s != nil {
		t.Fatal("disabled-cache write left reusable stale data", err)
	}
}

func TestCacheUnavailableSnapshotsUseTransientSharingAndPreserveFailures(t *testing.T) {
	c := &Client{repo: "owner/repo", cooldown: newAPICooldown("owner/repo", []byte(t.Name()))}
	c.cache = &snapshotCache{root: filepath.Join(t.TempDir(), "td", "gh-issue", "unavailable"), repo: c.repo, credential: cacheHash(t.Name())}
	// Existing file at the cache root makes persistent access unavailable.
	if err := os.MkdirAll(filepath.Dir(c.cache.root), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.cache.root, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	cause := errors.New("original request id HTTP 429")
	limit := &RateLimitError{Cause: cause, RetryAt: time.Now().Add(time.Hour), WaitSource: "retry-after"}
	calls := 0
	c.run = func(context.Context, string, []byte, ...string) ([]byte, error) { calls++; return nil, limit }
	if s, err := c.ReadSnapshot(context.Background(), true); s != nil || !errors.Is(err, cause) {
		t.Fatal("failure lost", err)
	}
	if s, err := c.ReadSnapshot(context.Background(), true); s != nil || !errors.Is(err, cause) || !strings.Contains(err.Error(), "no GitHub request was attempted") || calls != 1 {
		t.Fatal("failed collector retried during cooldown", err, calls)
	}
}

func TestProcessWriteFenceRejectsCachedBaselineWhenDiskInvalidationFailed(t *testing.T) {
	c, calls := cachedSnapshotFixture(t)
	if _, err := c.ReadSnapshot(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	// Simulate a write whose filesystem invalidation could not update the
	// generation/file: the process still knows that this baseline precedes it.
	c.snapshotReadGeneration(true)
	if _, err := c.ReadSnapshot(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 4 {
		t.Fatalf("known pre-write baseline reused: %v", *calls)
	}
}
